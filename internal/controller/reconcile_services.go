package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

func (r *MatrixDBClusterReconciler) reconcileServices(ctx context.Context, cluster *matrixdbv1alpha1.MatrixDBCluster) error {
	// 1. Headless Service for StatefulSet peer discovery (<cluster>-internal)
	internalSvcName := fmt.Sprintf("%s-internal", cluster.Name)
	if err := r.reconcileService(ctx, cluster, internalSvcName, corev1.ServiceTypeClusterIP, "None", map[string]string{
		constants.LabelAppName:      "matrixdb",
		constants.LabelAppInstance: cluster.Name,
	}); err != nil {
		return fmt.Errorf("failed to reconcile internal service: %w", err)
	}

	// 2. Primary Service (<cluster>-primary) routing to the writable master
	primarySvcName := fmt.Sprintf("%s-primary", cluster.Name)
	if err := r.reconcileService(ctx, cluster, primarySvcName, corev1.ServiceTypeClusterIP, "", map[string]string{
		constants.LabelAppName:      "matrixdb",
		constants.LabelAppInstance: cluster.Name,
		constants.LabelNodeRole:     constants.RolePrimary,
	}); err != nil {
		return fmt.Errorf("failed to reconcile primary service: %w", err)
	}

	// 3. Replica Service (<cluster>-replica) routing to read-only slaves
	replicaSvcName := fmt.Sprintf("%s-replica", cluster.Name)
	if err := r.reconcileService(ctx, cluster, replicaSvcName, corev1.ServiceTypeClusterIP, "", map[string]string{
		constants.LabelAppName:      "matrixdb",
		constants.LabelAppInstance: cluster.Name,
		constants.LabelNodeRole:     constants.RoleReplica,
	}); err != nil {
		return fmt.Errorf("failed to reconcile replica service: %w", err)
	}

	// 4. Dedicated Metrics Service (<cluster>-metrics) if metrics is enabled
	if cluster.Spec.Metrics != nil && cluster.Spec.Metrics.Enabled {
		metricsSvcName := fmt.Sprintf("%s-metrics", cluster.Name)
		if err := r.reconcileService(ctx, cluster, metricsSvcName, corev1.ServiceTypeClusterIP, "", map[string]string{
			constants.LabelAppName:      "matrixdb",
			constants.LabelAppInstance: cluster.Name,
		}); err != nil {
			return fmt.Errorf("failed to reconcile metrics service: %w", err)
		}
	}

	// 5. Dedicated Analytical Service (<cluster>-analytics)
	if cluster.Spec.AnalyticalReplicas > 0 {
		analyticsSvcName := fmt.Sprintf("%s-analytics", cluster.Name)
		if err := r.reconcileService(ctx, cluster, analyticsSvcName, corev1.ServiceTypeClusterIP, "", map[string]string{
			constants.LabelAppName:      "matrixdb",
			constants.LabelAppInstance: cluster.Name,
			"matrixdb.io/role-type":    "analytics",
		}); err != nil {
			return fmt.Errorf("failed to reconcile analytics service: %w", err)
		}
		cluster.Status.AnalyticalService = analyticsSvcName
	}

	return nil
}

func (r *MatrixDBClusterReconciler) reconcileService(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	name string,
	svcType corev1.ServiceType,
	clusterIP string,
	selector map[string]string,
) error {
	ports := []corev1.ServicePort{
		{
			Name:       "mysql",
			Port:       constants.MySQLPort,
			TargetPort: intstr.FromInt(constants.MySQLPort),
			Protocol:   corev1.ProtocolTCP,
		},
	}
	if cluster.Spec.Metrics != nil && cluster.Spec.Metrics.Enabled {
		mPort := int32(9104)
		if cluster.Spec.Metrics.Port > 0 {
			mPort = cluster.Spec.Metrics.Port
		}
		ports = append(ports, corev1.ServicePort{
			Name:       "metrics",
			Port:       mPort,
			TargetPort: intstr.FromInt(int(mPort)),
			Protocol:   corev1.ProtocolTCP,
		})
	}

	svc := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: name}, svc)
	if err != nil {
		if errors.IsNotFound(err) {
			newSvc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: cluster.Namespace,
					Labels: map[string]string{
						constants.LabelAppName:      "matrixdb",
						constants.LabelAppInstance: cluster.Name,
						constants.LabelClusterName:  cluster.Name,
						constants.LabelAppManagedBy: "matrixdb-operator",
					},
				},
				Spec: corev1.ServiceSpec{
					Type:      svcType,
					ClusterIP: clusterIP,
					Selector:  selector,
					Ports:     ports,
				},
			}
			if err := controllerutil.SetControllerReference(cluster, newSvc, r.Scheme); err != nil {
				return err
			}
			return r.Create(ctx, newSvc)
		}
		return err
	}

	// Update selector if changed (e.g. dynamic role target)
	updated := false
	for k, v := range selector {
		if svc.Spec.Selector[k] != v {
			svc.Spec.Selector[k] = v
			updated = true
		}
	}
	if len(svc.Spec.Ports) != len(ports) {
		svc.Spec.Ports = ports
		updated = true
	}
	if updated {
		return r.Update(ctx, svc)
	}
	return nil
}
