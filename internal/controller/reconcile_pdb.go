package controller

import (
	"context"
	"fmt"

	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

func (r *MatrixDBClusterReconciler) reconcilePDB(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
) error {
	pdbName := fmt.Sprintf("%s-pdb", cluster.Name)

	// Calculate majority minAvailable: e.g. for 3 replicas: (3/2) + 1 = 2
	minAvailableCount := int((cluster.Spec.Replicas / 2) + 1)
	if cluster.Spec.Replicas == 1 {
		minAvailableCount = 1
	}
	minAvailable := intstr.FromInt(minAvailableCount)

	existingPDB := &policyv1.PodDisruptionBudget{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: pdbName}, existingPDB)
	if err != nil {
		if errors.IsNotFound(err) {
			newPDB := &policyv1.PodDisruptionBudget{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pdbName,
					Namespace: cluster.Namespace,
					Labels: map[string]string{
						constants.LabelAppName:      "matrixdb",
						constants.LabelAppInstance: cluster.Name,
						constants.LabelClusterName:  cluster.Name,
						constants.LabelAppManagedBy: "matrixdb-operator",
					},
				},
				Spec: policyv1.PodDisruptionBudgetSpec{
					MinAvailable: &minAvailable,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							constants.LabelAppName:      "matrixdb",
							constants.LabelAppInstance: cluster.Name,
						},
					},
				},
			}
			if err := controllerutil.SetControllerReference(cluster, newPDB, r.Scheme); err != nil {
				return fmt.Errorf("failed setting controller reference on PDB: %w", err)
			}
			if err := r.Create(ctx, newPDB); err != nil {
				return fmt.Errorf("failed creating PDB %s: %w", pdbName, err)
			}
			r.Log.Info("Created PodDisruptionBudget for cluster protection", "pdb", pdbName, "minAvailable", minAvailableCount)
			return nil
		}
		return fmt.Errorf("failed getting PDB %s: %w", pdbName, err)
	}

	// Update MinAvailable if replicas changed
	if existingPDB.Spec.MinAvailable == nil || existingPDB.Spec.MinAvailable.IntValue() != minAvailableCount {
		existingPDB.Spec.MinAvailable = &minAvailable
		if err := r.Update(ctx, existingPDB); err != nil {
			return fmt.Errorf("failed updating PDB %s: %w", pdbName, err)
		}
		r.Log.Info("Updated PodDisruptionBudget minAvailable", "pdb", pdbName, "minAvailable", minAvailableCount)
	}

	return nil
}
