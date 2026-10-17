package controller

import (
	"context"
	"fmt"
	"strconv"
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
)

// MatrixDBScheduledBackupReconciler reconciles a MatrixDBScheduledBackup object
type MatrixDBScheduledBackupReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// Reconcile handles the scheduled execution of MatrixDBBackup resources
func (r *MatrixDBScheduledBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("scheduledbackup", req.NamespacedName)

	sched := &matrixdbv1alpha1.MatrixDBScheduledBackup{}
	if err := r.Get(ctx, req.NamespacedName, sched); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 1. Verify target cluster exists
	cluster := &matrixdbv1alpha1.MatrixDBCluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: sched.Namespace, Name: sched.Spec.ClusterName}, cluster); err != nil {
		log.Info("Target cluster does not exist yet", "cluster", sched.Spec.ClusterName)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	now := time.Now().UTC()

	// 2. Determine if it's time to trigger a new backup
	shouldRun := false
	if sched.Status.LastBackupTime == nil {
		shouldRun = true
	} else {
		interval := parseScheduleInterval(sched.Spec.Schedule)
		if now.Sub(sched.Status.LastBackupTime.Time) >= interval {
			shouldRun = true
		}
	}

	if shouldRun {
		backupName := fmt.Sprintf("%s-%d", sched.Name, now.Unix())
		newBackup := &matrixdbv1alpha1.MatrixDBBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backupName,
				Namespace: sched.Namespace,
				Labels: map[string]string{
					"matrixdb.io/cluster":   sched.Spec.ClusterName,
					"matrixdb.io/scheduled": sched.Name,
				},
			},
			Spec: matrixdbv1alpha1.MatrixDBBackupSpec{
				ClusterName: sched.Spec.ClusterName,
				Storage:     sched.Spec.Storage,
			},
		}

		if err := r.Create(ctx, newBackup); err != nil {
			log.Error(err, "Failed creating scheduled MatrixDBBackup", "name", backupName)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}

		log.Info("Successfully triggered scheduled backup", "backup", backupName)
		r.Recorder.Eventf(sched, corev1.EventTypeNormal, "BackupTriggered", "Triggered backup %s", backupName)

		tNow := metav1.NewTime(now)
		sched.Status.LastBackupTime = &tNow
		sched.Status.LastBackupName = backupName
		if err := r.Status().Update(ctx, sched); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Apply retention policy (cleanup older backups)
	if sched.Spec.RetentionDays > 0 {
		var backupList matrixdbv1alpha1.MatrixDBBackupList
		if err := r.List(ctx, &backupList, client.InNamespace(sched.Namespace), client.MatchingLabels{
			"matrixdb.io/scheduled": sched.Name,
		}); err == nil {
			sched.Status.BackupsCount = len(backupList.Items)
			cutoff := now.Add(-time.Duration(sched.Spec.RetentionDays) * 24 * time.Hour)
			for _, bk := range backupList.Items {
				if bk.CreationTimestamp.Time.Before(cutoff) {
					log.Info("Cleaning up expired backup", "backup", bk.Name, "created", bk.CreationTimestamp)
					_ = r.Delete(ctx, &bk)
				}
			}
			_ = r.Status().Update(ctx, sched)
		}
	}

	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// parseScheduleInterval determines how often to run
func parseScheduleInterval(schedule string) time.Duration {
	if strings.HasPrefix(schedule, "*/") {
		parts := strings.Split(schedule, " ")
		minutePart := strings.TrimPrefix(parts[0], "*/")
		if mins, err := strconv.Atoi(minutePart); err == nil && mins > 0 {
			return time.Duration(mins) * time.Minute
		}
	}
	if schedule == "@hourly" {
		return 1 * time.Hour
	}
	if schedule == "@daily" {
		return 24 * time.Hour
	}
	// Default to 1 hour
	return 1 * time.Hour
}

// SetupWithManager registers the reconciler with controller manager
func (r *MatrixDBScheduledBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&matrixdbv1alpha1.MatrixDBScheduledBackup{}).
		Complete(r)
}
