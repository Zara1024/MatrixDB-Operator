package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
)

// validateAdmissionGuardrails enforces cluster safety constraints aligned with enterprise standards.
func (r *MatrixDBClusterReconciler) validateAdmissionGuardrails(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
) error {
	// Guardrail 1: Disallow replica count of 0 (prevent total quorum collapse)
	if cluster.Spec.Replicas < 1 {
		r.Recorder.Eventf(cluster, corev1.EventTypeWarning, "GuardrailBlocked",
			"Replicas count cannot be %d. Minimum quorum requires at least 1 instance.", cluster.Spec.Replicas)
		r.setCondition(cluster, "Admitted", metav1.ConditionFalse, "InvalidReplicas",
			fmt.Sprintf("Replicas count %d is invalid: minimum 1 required", cluster.Spec.Replicas))
		return fmt.Errorf("admission guardrail blocked: replicas cannot be less than 1")
	}

	// Check existing StatefulSet if present
	existingSts := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}, existingSts)
	if err != nil {
		if errors.IsNotFound(err) {
			// First-time creation is valid
			return nil
		}
		return err
	}

	// Guardrail 2: StorageClass Immutability
	if len(existingSts.Spec.VolumeClaimTemplates) > 0 {
		existingSC := existingSts.Spec.VolumeClaimTemplates[0].Spec.StorageClassName
		desiredSC := cluster.Spec.Storage.StorageClassName
		if existingSC != nil && desiredSC != nil && *existingSC != *desiredSC {
			r.Recorder.Eventf(cluster, corev1.EventTypeWarning, "GuardrailBlocked",
				"StorageClassName is immutable. Modification from '%s' to '%s' rejected.", *existingSC, *desiredSC)
			r.setCondition(cluster, "Admitted", metav1.ConditionFalse, "StorageClassImmutable",
				"StorageClassName cannot be modified after initial cluster creation")
			return fmt.Errorf("admission guardrail blocked: storageClassName is immutable")
		}
	}

	// Guardrail 3: Safe Scale-Down Quorum Protection
	if existingSts.Spec.Replicas != nil && cluster.Spec.Replicas < *existingSts.Spec.Replicas {
		if existingSts.Status.ReadyReplicas == 0 {
			r.Recorder.Eventf(cluster, corev1.EventTypeWarning, "GuardrailBlocked",
				"Scale-down rejected: cluster currently has 0 ready instances")
			r.setCondition(cluster, "Admitted", metav1.ConditionFalse, "ScaleDownBlocked",
				"Cannot scale down while 0 instances are ready")
			return fmt.Errorf("admission guardrail blocked: cannot scale down degraded cluster")
		}
	}

	return nil
}
