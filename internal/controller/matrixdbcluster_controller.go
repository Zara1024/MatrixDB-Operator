package controller

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
)

// MatrixDBClusterReconciler reconciles a MatrixDBCluster object
type MatrixDBClusterReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=apps.matrixdb.io,resources=matrixdbclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.matrixdb.io,resources=matrixdbclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.matrixdb.io,resources=matrixdbclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the core declarative control loop for MatrixDBCluster.
func (r *MatrixDBClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("matrixdbcluster", req.NamespacedName)

	cluster := &matrixdbv1alpha1.MatrixDBCluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		if errors.IsNotFound(err) {
			log.Info("MatrixDBCluster resource deleted, skipping reconcile")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get MatrixDBCluster")
		return ctrl.Result{}, err
	}

	// 0. Validate admission guardrails (Safety checks)
	if err := r.validateAdmissionGuardrails(ctx, cluster); err != nil {
		log.Error(err, "Admission guardrail validation failed")
		_ = r.Status().Update(ctx, cluster)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// 1. Reconcile Secrets (Root and Replica passwords)
	creds, err := r.reconcileSecret(ctx, cluster)
	if err != nil {
		log.Error(err, "Failed reconciling cluster Secret")
		return ctrl.Result{}, err
	}

	// 2. Reconcile ConfigMap (my.cnf with semi-sync and GTID)
	cmName, err := r.reconcileConfigMap(ctx, cluster)
	if err != nil {
		log.Error(err, "Failed reconciling cluster ConfigMap")
		return ctrl.Result{}, err
	}

	// 3. Reconcile Services (Headless internal, Primary, Replica)
	if err := r.reconcileServices(ctx, cluster); err != nil {
		log.Error(err, "Failed reconciling cluster Services")
		return ctrl.Result{}, err
	}

	// 3b. Reconcile PodDisruptionBudget
	if err := r.reconcilePDB(ctx, cluster); err != nil {
		log.Error(err, "Failed reconciling PodDisruptionBudget")
		return ctrl.Result{}, err
	}

	// 4. Reconcile StatefulSet
	sts, err := r.reconcileStatefulSet(ctx, cluster, creds, cmName)
	if err != nil {
		log.Error(err, "Failed reconciling StatefulSet")
		return ctrl.Result{}, err
	}

	// 5. Reconcile MySQL Replication Topology and HA Failover
	if err := r.reconcileTopology(ctx, cluster, creds); err != nil {
		log.Error(err, "Failed reconciling cluster replication topology")
		// Update status condition to degraded
		r.setCondition(cluster, "ReplicationHealthy", metav1.ConditionFalse, "TopologyError", err.Error())
		_ = r.Status().Update(ctx, cluster)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	// 5b. Reconcile Dynamic Configuration via SET PERSIST (No Pod restart)
	if err := r.reconcileDynamicConfig(ctx, cluster, creds); err != nil {
		log.Error(err, "Failed reconciling dynamic configuration")
	}

	// 5c. Reconcile ProxySQL Traffic Routing
	if err := r.reconcileProxySQL(ctx, cluster, creds); err != nil {
		log.Error(err, "Failed reconciling ProxySQL layer")
	}

	// 5d. Reconcile Online Volume Expansion
	if err := r.ReconcileVolumeExpansion(ctx, cluster); err != nil {
		log.Error(err, "Failed reconciling volume expansion")
	}

	// 5e. Reconcile Autonomous DBA Doctor (MDL Killer & Lag Optimizer)
	if err := r.ReconcileDBADoctor(ctx, cluster); err != nil {
		log.Error(err, "Failed reconciling DBA Doctor")
	}

	// 5f. Reconcile Replica-First Rolling Upgrade
	if err := r.ReconcileRollingUpgrade(ctx, cluster); err != nil {
		log.Error(err, "Failed reconciling rolling upgrade")
	}

	// 5g. Reconcile Continuous Binlog Archiver (PITR)
	if err := r.ReconcileBinlogArchiver(ctx, cluster); err != nil {
		log.Error(err, "Failed reconciling binlog archiver")
	}

	// 6. Update Conditions and Status
	isReady := sts.Status.ReadyReplicas == cluster.Spec.Replicas
	if isReady {
		r.setCondition(cluster, "Ready", metav1.ConditionTrue, "AllPodsReady", "All MySQL instances are healthy and ready")
	} else {
		r.setCondition(cluster, "Ready", metav1.ConditionFalse, "WaitingPods", "Not all MySQL instances have become ready")
	}

	if cluster.Status.SyncedReplicas >= (cluster.Spec.Replicas - 1) {
		r.setCondition(cluster, "ReplicationHealthy", metav1.ConditionTrue, "Replicating", "All replicas are actively synchronizing via semi-sync")
	} else {
		r.setCondition(cluster, "ReplicationHealthy", metav1.ConditionFalse, "ReplicasNotSynced", "Some replicas are not actively synchronizing")
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &matrixdbv1alpha1.MatrixDBCluster{}
		if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
			return err
		}
		latest.Status = cluster.Status
		return r.Status().Update(ctx, latest)
	}); err != nil {
		log.Error(err, "Failed to update MatrixDBCluster status")
		return ctrl.Result{}, err
	}

	// Non-blocking proactive poll interval (every 10s for continuous health checks)
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *MatrixDBClusterReconciler) setCondition(
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	now := metav1.Now()
	for i, existing := range cluster.Status.Conditions {
		if existing.Type == condType {
			if existing.Status != status || existing.Reason != reason || existing.Message != message {
				cluster.Status.Conditions[i].Status = status
				cluster.Status.Conditions[i].Reason = reason
				cluster.Status.Conditions[i].Message = message
				cluster.Status.Conditions[i].LastTransitionTime = now
			}
			return
		}
	}
	cluster.Status.Conditions = append(cluster.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *MatrixDBClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&matrixdbv1alpha1.MatrixDBCluster{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Complete(r)
}
