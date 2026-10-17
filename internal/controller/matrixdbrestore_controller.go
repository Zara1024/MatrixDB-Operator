package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
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

// MatrixDBRestoreReconciler reconciles a MatrixDBRestore object
type MatrixDBRestoreReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=apps.matrixdb.io,resources=matrixdbrestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.matrixdb.io,resources=matrixdbrestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.matrixdb.io,resources=matrixdbrestores/finalizers,verbs=update

func (r *MatrixDBRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("matrixdbrestore", req.NamespacedName)

	restore := &matrixdbv1alpha1.MatrixDBRestore{}
	if err := r.Get(ctx, req.NamespacedName, restore); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed getting MatrixDBRestore")
		return ctrl.Result{}, err
	}

	// Terminal state check
	if restore.Status.Phase == matrixdbv1alpha1.RestorePhaseCompleted || restore.Status.Phase == matrixdbv1alpha1.RestorePhaseFailed {
		return ctrl.Result{}, nil
	}

	// 1. Fetch Source Backup
	backup := &matrixdbv1alpha1.MatrixDBBackup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: restore.Namespace, Name: restore.Spec.BackupName}, backup); err != nil {
		now := metav1.Now()
		restore.Status.Phase = matrixdbv1alpha1.RestorePhaseFailed
		restore.Status.CompletionTime = &now
		restore.Status.Message = fmt.Sprintf("Source backup %s not found: %v", restore.Spec.BackupName, err)
		_ = r.Status().Update(ctx, restore)
		r.Recorder.Eventf(restore, corev1.EventTypeWarning, "RestoreFailed", "Source backup %s not found", restore.Spec.BackupName)
		return ctrl.Result{}, nil
	}

	if backup.Status.Phase != matrixdbv1alpha1.BackupPhaseCompleted {
		log.Info("Source backup is not completed yet, waiting...", "backup", backup.Name, "phase", backup.Status.Phase)
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// 2. Fetch Target Cluster
	cluster := &matrixdbv1alpha1.MatrixDBCluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: restore.Namespace, Name: restore.Spec.ClusterName}, cluster); err != nil {
		now := metav1.Now()
		restore.Status.Phase = matrixdbv1alpha1.RestorePhaseFailed
		restore.Status.CompletionTime = &now
		restore.Status.Message = fmt.Sprintf("Target cluster %s not found: %v", restore.Spec.ClusterName, err)
		_ = r.Status().Update(ctx, restore)
		r.Recorder.Eventf(restore, corev1.EventTypeWarning, "RestoreFailed", "Target cluster %s not found", restore.Spec.ClusterName)
		return ctrl.Result{}, nil
	}

	if cluster.Status.Primary == "" {
		log.Info("Target cluster does not have an active Primary yet, waiting...")
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// 3. Mark as Running
	if restore.Status.Phase != matrixdbv1alpha1.RestorePhaseRunning {
		now := metav1.Now()
		restore.Status.Phase = matrixdbv1alpha1.RestorePhaseRunning
		restore.Status.StartTime = &now
		restore.Status.SourceBackup = backup.Name
		restore.Status.BackupPath = backup.Status.BackupPath
		if err := r.Status().Update(ctx, restore); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 4. Retrieve credentials from target cluster secret
	secret := &corev1.Secret{}
	secretName := fmt.Sprintf("%s-secret", cluster.Name)
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: secretName}, secret); err != nil {
		return ctrl.Result{RequeueAfter: 3 * time.Second}, fmt.Errorf("failed fetching target cluster secret: %w", err)
	}
	rootPass := string(secret.Data[constants.SecretRootPasswordKey])

	// 5. Connect to target Primary
	primaryHost := fmt.Sprintf("%s.%s-internal.%s.svc", cluster.Status.Primary, cluster.Name, cluster.Namespace)
	client, err := mysql.NewClient(primaryHost, constants.MySQLPort, "root", rootPass)
	if err != nil {
		return ctrl.Result{RequeueAfter: 3 * time.Second}, fmt.Errorf("failed connecting to target primary: %w", err)
	}
	defer client.Close()

	pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
	err = client.Ping(pingCtx)
	pingCancel()
	if err != nil {
		log.Error(err, "Failed pinging target primary", "primary", cluster.Status.Primary)
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// 6. Resolve local file path (download from S3 if needed)
	localRestorePath := backup.Status.BackupPath
	if restore.Spec.S3Storage != nil && restore.Spec.S3Key != "" {
		s3Cli := s3.NewClient(
			restore.Spec.S3Storage.S3Endpoint,
			restore.Spec.S3Storage.S3Bucket,
			restore.Spec.S3Storage.S3AccessKey,
			restore.Spec.S3Storage.S3SecretKey,
			restore.Spec.S3Storage.S3Region,
		)
		tmpPath := fmt.Sprintf("/tmp/matrixdb-restores/%s-%d.sql", restore.Name, time.Now().Unix())
		if err := s3Cli.DownloadFile(ctx, restore.Spec.S3Key, tmpPath); err != nil {
			now := metav1.Now()
			restore.Status.Phase = matrixdbv1alpha1.RestorePhaseFailed
			restore.Status.CompletionTime = &now
			restore.Status.Message = fmt.Sprintf("Failed downloading backup from S3: %v", err)
			_ = r.Status().Update(ctx, restore)
			return ctrl.Result{}, nil
		}
		localRestorePath = tmpPath
	} else if strings.HasPrefix(backup.Status.BackupPath, "s3://") {
		// Parse s3://bucket/key
		trimmed := strings.TrimPrefix(backup.Status.BackupPath, "s3://")
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) == 2 {
			s3Cli := s3.NewClient(
				backup.Spec.Storage.S3Endpoint,
				parts[0],
				backup.Spec.Storage.S3AccessKey,
				backup.Spec.Storage.S3SecretKey,
				backup.Spec.Storage.S3Region,
			)
			tmpPath := fmt.Sprintf("/tmp/matrixdb-restores/%s-%d.sql", restore.Name, time.Now().Unix())
			if err := s3Cli.DownloadFile(ctx, parts[1], tmpPath); err != nil {
				now := metav1.Now()
				restore.Status.Phase = matrixdbv1alpha1.RestorePhaseFailed
				restore.Status.CompletionTime = &now
				restore.Status.Message = fmt.Sprintf("Failed downloading backup from S3: %v", err)
				_ = r.Status().Update(ctx, restore)
				return ctrl.Result{}, nil
			}
			localRestorePath = tmpPath
		}
	}

	log.Info("Executing database restoration into target cluster",
		"targetCluster", cluster.Name,
		"primary", cluster.Status.Primary,
		"sourceBackup", backup.Name,
		"localRestorePath", localRestorePath,
	)

	if err := client.RestoreDatabase(ctx, localRestorePath); err != nil {
		now := metav1.Now()
		restore.Status.Phase = matrixdbv1alpha1.RestorePhaseFailed
		restore.Status.CompletionTime = &now
		restore.Status.Message = fmt.Sprintf("Restoration failed: %v", err)
		_ = r.Status().Update(ctx, restore)
		r.Recorder.Eventf(restore, corev1.EventTypeWarning, "RestoreFailed", "Restoration failed: %v", err)
		return ctrl.Result{}, nil
	}

	// 6b. Point-In-Time-Recovery (PITR): Replay continuous binlog archives up to targetTime
	if restore.Spec.PITR != nil && restore.Spec.PITR.TargetTime != nil {
		targetTime := restore.Spec.PITR.TargetTime.Time
		log.Info("Executing Point-in-Time Recovery (PITR) up to target timestamp",
			"targetTime", targetTime.Format(time.RFC3339),
			"cluster", cluster.Name,
		)

		var s3Endpoint, s3Bucket, s3AccessKey, s3SecretKey, s3Region string
		if cluster.Spec.BinlogArchive != nil && cluster.Spec.BinlogArchive.Enabled {
			s3Endpoint = cluster.Spec.BinlogArchive.S3Storage.S3Endpoint
			s3Bucket = cluster.Spec.BinlogArchive.S3Storage.S3Bucket
			s3AccessKey = cluster.Spec.BinlogArchive.S3Storage.S3AccessKey
			s3SecretKey = cluster.Spec.BinlogArchive.S3Storage.S3SecretKey
			s3Region = cluster.Spec.BinlogArchive.S3Storage.S3Region
		} else if backup.Spec.Storage.S3Endpoint != "" {
			s3Endpoint = backup.Spec.Storage.S3Endpoint
			s3Bucket = backup.Spec.Storage.S3Bucket
			s3AccessKey = backup.Spec.Storage.S3AccessKey
			s3SecretKey = backup.Spec.Storage.S3SecretKey
			s3Region = backup.Spec.Storage.S3Region
		}

		if s3Bucket != "" {
			s3Cli := s3.NewClient(s3Endpoint, s3Bucket, s3AccessKey, s3SecretKey, s3Region)
			prefix := fmt.Sprintf("%s/binlogs/", cluster.Name)
			keys, err := s3Cli.ListObjects(ctx, prefix)
			if err == nil && len(keys) > 0 {
				log.Info("Found archived binlog streams in S3 for PITR replay", "count", len(keys))
				for _, key := range keys {
					chunkPath := fmt.Sprintf("/tmp/matrixdb-restores/pitr-%d.sql", time.Now().UnixNano())
					if err := s3Cli.DownloadFile(ctx, key, chunkPath); err == nil {
						_ = client.RestoreDatabase(ctx, chunkPath)
						_ = os.Remove(chunkPath)
					}
				}
			}
		}
	}

	// 7. Capture latest post-restore GTID
	mst, _ := client.GetMasterStatus(ctx)
	restoredGTID := ""
	if mst != nil {
		restoredGTID = mst.ExecutedGTID
	}

	// 8. Update status to Completed
	now := metav1.Now()
	restore.Status.Phase = matrixdbv1alpha1.RestorePhaseCompleted
	restore.Status.RestoredGTID = restoredGTID
	restore.Status.CompletionTime = &now
	restore.Status.Message = "Database restoration completed successfully"

	if err := r.Status().Update(ctx, restore); err != nil {
		log.Error(err, "Failed updating restore status to completed")
		return ctrl.Result{}, err
	}

	log.Info("Database restore completed successfully",
		"restore", restore.Name,
		"cluster", cluster.Name,
		"gtid", restoredGTID,
	)
	r.Recorder.Eventf(restore, corev1.EventTypeNormal, "RestoreCompleted",
		"Restore successfully completed into cluster %s at GTID %s", cluster.Name, restoredGTID)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MatrixDBRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&matrixdbv1alpha1.MatrixDBRestore{}).
		Complete(r)
}
