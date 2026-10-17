package controller

import (
	"context"
	"fmt"
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
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

// MatrixDBSchemaMigrationReconciler reconciles a MatrixDBSchemaMigration object
type MatrixDBSchemaMigrationReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// Reconcile executes declarative DDL schema migrations against the cluster Primary
func (r *MatrixDBSchemaMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("schemamigration", req.NamespacedName)

	migration := &matrixdbv1alpha1.MatrixDBSchemaMigration{}
	if err := r.Get(ctx, req.NamespacedName, migration); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Idempotency: skip if already terminal
	if migration.Status.Phase == matrixdbv1alpha1.MigrationPhaseCompleted ||
		migration.Status.Phase == matrixdbv1alpha1.MigrationPhaseFailed {
		return ctrl.Result{}, nil
	}

	// 1. Fetch target cluster
	cluster := &matrixdbv1alpha1.MatrixDBCluster{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: migration.Namespace, Name: migration.Spec.ClusterName}, cluster); err != nil {
		now := metav1.Now()
		migration.Status.Phase = matrixdbv1alpha1.MigrationPhaseFailed
		migration.Status.ExecutedAt = &now
		migration.Status.Message = fmt.Sprintf("Cluster %s not found: %v", migration.Spec.ClusterName, err)
		_ = r.Status().Update(ctx, migration)
		return ctrl.Result{}, nil
	}

	if cluster.Status.Primary == "" {
		log.Info("Cluster has no active Primary yet, waiting...")
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// 2. Mark as Running
	if migration.Status.Phase != matrixdbv1alpha1.MigrationPhaseRunning {
		migration.Status.Phase = matrixdbv1alpha1.MigrationPhaseRunning
		if err := r.Status().Update(ctx, migration); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Connect to Primary
	secret := &corev1.Secret{}
	secretName := fmt.Sprintf("%s-secret", cluster.Name)
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: secretName}, secret); err != nil {
		return ctrl.Result{RequeueAfter: 3 * time.Second}, fmt.Errorf("failed fetching secret: %w", err)
	}
	rootPass := string(secret.Data[constants.SecretRootPasswordKey])

	primaryHost := fmt.Sprintf("%s.%s-internal.%s.svc", cluster.Status.Primary, cluster.Name, cluster.Namespace)
	myClient, err := mysql.NewClient(primaryHost, constants.MySQLPort, "root", rootPass)
	if err != nil {
		return ctrl.Result{RequeueAfter: 3 * time.Second}, err
	}
	defer myClient.Close()

	// 4. Ensure target database exists
	if migration.Spec.Database != "" {
		_, _ = myClient.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", migration.Spec.Database))
		_, _ = myClient.ExecContext(ctx, fmt.Sprintf("USE `%s`", migration.Spec.Database))
	}

	// 5. Execute DDL statements
	start := time.Now()
	statements := splitStatements(migration.Spec.SQL)
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		log.Info("Executing DDL statement", "sql", stmt)
		if _, err := myClient.ExecContext(ctx, stmt); err != nil {
			now := metav1.Now()
			migration.Status.Phase = matrixdbv1alpha1.MigrationPhaseFailed
			migration.Status.ExecutedAt = &now
			migration.Status.Message = fmt.Sprintf("DDL execution failed on '%s': %v", stmt, err)
			_ = r.Status().Update(ctx, migration)
			r.Recorder.Eventf(migration, corev1.EventTypeWarning, "MigrationFailed", "DDL error: %v", err)
			return ctrl.Result{}, nil
		}
	}
	duration := time.Since(start).Round(time.Millisecond).String()

	// 6. Mark Completed
	now := metav1.Now()
	migration.Status.Phase = matrixdbv1alpha1.MigrationPhaseCompleted
	migration.Status.ExecutedAt = &now
	migration.Status.ExecutionDuration = duration
	migration.Status.ExecutedBy = cluster.Status.Primary
	migration.Status.Message = fmt.Sprintf("Successfully applied %d DDL statements", len(statements))

	if err := r.Status().Update(ctx, migration); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("DDL migration completed successfully", "duration", duration, "executedBy", cluster.Status.Primary)
	r.Recorder.Eventf(migration, corev1.EventTypeNormal, "MigrationApplied", "DDL applied successfully in %s", duration)
	return ctrl.Result{}, nil
}

func splitStatements(sqlScript string) []string {
	var stmts []string
	raw := strings.Split(sqlScript, ";")
	for _, s := range raw {
		clean := strings.TrimSpace(s)
		if clean != "" {
			stmts = append(stmts, clean)
		}
	}
	return stmts
}

// SetupWithManager registers the reconciler
func (r *MatrixDBSchemaMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&matrixdbv1alpha1.MatrixDBSchemaMigration{}).
		Complete(r)
}
