package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/internal/mysql"
	"github.com/matrixdb-io/matrixdb-operator/internal/s3"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

// MatrixDBBackupReconciler reconciles a MatrixDBBackup object
type MatrixDBBackupReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=apps.matrixdb.io,resources=matrixdbbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.matrixdb.io,resources=matrixdbbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.matrixdb.io,resources=matrixdbbackups/finalizers,verbs=update

func (r *MatrixDBBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("matrixdbbackup", req.NamespacedName)

	backup := &matrixdbv1alpha1.MatrixDBBackup{}
	if err := r.Get(ctx, req.NamespacedName, backup); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed getting MatrixDBBackup")
		return ctrl.Result{}, err
	}

	// If already in terminal state, no further action needed
	if backup.Status.Phase == matrixdbv1alpha1.BackupPhaseCompleted || backup.Status.Phase == matrixdbv1alpha1.BackupPhaseFailed {
		return ctrl.Result{}, nil
	}

	// 1. Fetch target cluster
	cluster := &matrixdbv1alpha1.MatrixDBCluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: backup.Spec.ClusterName}, cluster); err != nil {
		now := metav1.Now()
		backup.Status.Phase = matrixdbv1alpha1.BackupPhaseFailed
		backup.Status.CompletionTime = &now
		backup.Status.Message = fmt.Sprintf("Cluster %s not found: %v", backup.Spec.ClusterName, err)
		_ = r.Status().Update(ctx, backup)
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "BackupFailed", "Cluster %s not found", backup.Spec.ClusterName)
		return ctrl.Result{}, nil
	}

	// 2. Mark as Running if still pending
	if backup.Status.Phase != matrixdbv1alpha1.BackupPhaseRunning {
		now := metav1.Now()
		backup.Status.Phase = matrixdbv1alpha1.BackupPhaseRunning
		backup.Status.StartTime = &now
		if err := r.Status().Update(ctx, backup); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Retrieve root credentials from cluster Secret
	secret := &corev1.Secret{}
	secretName := fmt.Sprintf("%s-secret", cluster.Name)
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: secretName}, secret); err != nil {
		return ctrl.Result{RequeueAfter: 3 * time.Second}, fmt.Errorf("failed fetching cluster secret: %w", err)
	}
	rootPass := string(secret.Data[constants.SecretRootPasswordKey])

	// 4. Select source Pod: prefer healthy, non-errant replica to offload primary
	sourcePod := ""
	for _, n := range cluster.Status.Nodes {
		if n.Role == matrixdbv1alpha1.NodeRoleReplica && n.Ready && !n.Errant {
			sourcePod = n.PodName
			break
		}
	}
	if sourcePod == "" {
		sourcePod = cluster.Status.Primary
	}
	if sourcePod == "" {
		log.Info("No available pod found for backup yet, requeueing...")
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// 5. Connect to source MySQL instance
	host := fmt.Sprintf("%s.%s-internal.%s.svc", sourcePod, cluster.Name, cluster.Namespace)
	client, err := mysql.NewClient(host, constants.MySQLPort, "root", rootPass)
	if err != nil {
		return ctrl.Result{RequeueAfter: 3 * time.Second}, fmt.Errorf("failed creating mysql client for backup: %w", err)
	}
	defer client.Close()

	pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
	err = client.Ping(pingCtx)
	pingCancel()
	if err != nil {
		log.Error(err, "Failed pinging backup source node", "node", sourcePod)
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// 6. Record GTID and Binlog coordinates for Point-In-Time-Recovery (PITR)
	executedGTID := ""
	var binlogFile string
	var binlogPos uint64
	mst, err := client.GetMasterStatus(ctx)
	if err == nil && mst != nil {
		executedGTID = mst.ExecutedGTID
		binlogFile = mst.File
		binlogPos = mst.Position
	}
	if executedGTID == "" {
		rep, _ := client.GetReplicaStatus(ctx)
		if rep != nil {
			executedGTID = rep.ExecutedGTID
		}
	}

	// 7. Execute consistent database logical snapshot dump
	targetPath := backup.Spec.Storage.LocalPath
	if targetPath == "" {
		targetPath = fmt.Sprintf("/tmp/matrixdb-backups/%s-%s.sql", backup.Name, time.Now().Format("20060102-150405"))
	}

	log.Info("Starting snapshot backup", "sourcePod", sourcePod, "targetPath", targetPath)
	if err := client.DumpDatabases(ctx, targetPath); err != nil {
		now := metav1.Now()
		backup.Status.Phase = matrixdbv1alpha1.BackupPhaseFailed
		backup.Status.CompletionTime = &now
		backup.Status.Message = fmt.Sprintf("Dump failed: %v", err)
		_ = r.Status().Update(ctx, backup)
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "BackupFailed", "Dump failed: %v", err)
		return ctrl.Result{}, nil
	}

	// 8. Calculate backup artifact size
	var sizeStr string
	if fi, err := os.Stat(targetPath); err == nil {
		sizeStr = formatBytes(fi.Size())
	} else {
		sizeStr = "unknown"
	}

	// 8.1 S3 Upload if configured
	backupURI := targetPath
	if backup.Spec.Storage.S3Bucket != "" {
		s3Cli := s3.NewClient(
			backup.Spec.Storage.S3Endpoint,
			backup.Spec.Storage.S3Bucket,
			backup.Spec.Storage.S3AccessKey,
			backup.Spec.Storage.S3SecretKey,
			backup.Spec.Storage.S3Region,
		)
		_ = s3Cli.EnsureBucket(ctx)
		s3Key := fmt.Sprintf("%s/%s", backup.Spec.ClusterName, filepath.Base(targetPath))
		if err := s3Cli.UploadFile(ctx, s3Key, targetPath); err != nil {
			log.Error(err, "Failed uploading backup to S3", "s3Key", s3Key)
		} else {
			log.Info("Successfully uploaded backup to S3", "s3Bucket", backup.Spec.Storage.S3Bucket, "s3Key", s3Key)
			backupURI = fmt.Sprintf("s3://%s/%s", backup.Spec.Storage.S3Bucket, s3Key)
		}
	}

	// 9. Update Status to Completed
	now := metav1.Now()
	backup.Status.Phase = matrixdbv1alpha1.BackupPhaseCompleted
	backup.Status.BackupPath = backupURI
	backup.Status.BackupSize = sizeStr
	backup.Status.ExecutedGTID = executedGTID
	backup.Status.BinlogFile = binlogFile
	backup.Status.BinlogPos = binlogPos
	backup.Status.SourcePod = sourcePod
	backup.Status.CompletionTime = &now
	backup.Status.Message = "Backup snapshot completed successfully"

	if err := r.Status().Update(ctx, backup); err != nil {
		log.Error(err, "Failed updating backup status to completed")
		return ctrl.Result{}, err
	}

	log.Info("Snapshot backup successfully completed",
		"backup", backup.Name,
		"size", sizeStr,
		"gtid", executedGTID,
		"binlogFile", binlogFile,
		"binlogPos", binlogPos,
	)
	r.Recorder.Eventf(backup, corev1.EventTypeNormal, "BackupCompleted",
		"Backup successfully completed at GTID %s (size: %s)", executedGTID, sizeStr)

	return ctrl.Result{}, nil
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// SetupWithManager sets up the controller with the Manager.
func (r *MatrixDBBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&matrixdbv1alpha1.MatrixDBBackup{}).
		Complete(r)
}
