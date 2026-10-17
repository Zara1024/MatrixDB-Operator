package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
)

// ReconcileVolumeExpansion inspects PVCs and performs online volume expansion when cluster.Spec.Storage.Size increases
func (r *MatrixDBClusterReconciler) ReconcileVolumeExpansion(ctx context.Context, cluster *matrixdbv1alpha1.MatrixDBCluster) error {
	log := r.Log.WithValues("cluster", cluster.Name, "stage", "volume-expansion")

	desiredSizeStr := cluster.Spec.Storage.Size
	if desiredSizeStr == "" {
		return nil
	}

	desiredQty, err := resource.ParseQuantity(desiredSizeStr)
	if err != nil {
		log.Error(err, "Invalid storage size specified in cluster spec", "size", desiredSizeStr)
		return nil
	}

	// List all PVCs for this cluster
	var pvcList corev1.PersistentVolumeClaimList
	selector := labels.SelectorFromSet(map[string]string{
		"app.kubernetes.io/name":     "matrixdb",
		"app.kubernetes.io/instance": cluster.Name,
	})

	if err := r.List(ctx, &pvcList, client.InNamespace(cluster.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return fmt.Errorf("failed listing cluster PVCs: %w", err)
	}

	for _, pvc := range pvcList.Items {
		currentQty := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		if currentQty.Cmp(desiredQty) < 0 {
			log.Info("Online Volume Expansion requested for PVC",
				"pvc", pvc.Name,
				"current", currentQty.String(),
				"desired", desiredQty.String(),
			)

			pvcCopy := pvc.DeepCopy()
			pvcCopy.Spec.Resources.Requests[corev1.ResourceStorage] = desiredQty
			if err := r.Update(ctx, pvcCopy); err != nil {
				log.Error(err, "Failed expanding PVC size", "pvc", pvc.Name)
				r.Recorder.Eventf(cluster, corev1.EventTypeWarning, "VolumeExpansionFailed",
					"Failed expanding PVC %s to %s: %v", pvc.Name, desiredSizeStr, err)
			} else {
				log.Info("PVC storage request updated successfully", "pvc", pvc.Name, "newSize", desiredSizeStr)
				r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "VolumeExpanded",
					"Expanded PVC %s from %s to %s online (no restart required)", pvc.Name, currentQty.String(), desiredSizeStr)
			}
		}
	}

	return nil
}
