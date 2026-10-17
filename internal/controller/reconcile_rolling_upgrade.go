package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

// ReconcileRollingUpgrade orchestrates Replica-First rolling upgrades
func (r *MatrixDBClusterReconciler) ReconcileRollingUpgrade(ctx context.Context, cluster *matrixdbv1alpha1.MatrixDBCluster) error {
	log := r.Log.WithValues("cluster", cluster.Name, "stage", "rolling-upgrade")

	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}, sts); err != nil {
		return nil
	}

	currentImage := ""
	for _, c := range sts.Spec.Template.Spec.Containers {
		if c.Name == "mysqld" {
			currentImage = c.Image
			break
		}
	}

	desiredImage := cluster.Spec.Image
	if desiredImage == "" || currentImage == desiredImage {
		cluster.Status.Upgrading = false
		return nil
	}

	cluster.Status.Upgrading = true
	log.Info("Replica-First Rolling Upgrade in progress", "currentImage", currentImage, "desiredImage", desiredImage)

	// List cluster pods
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(cluster.Namespace), client.MatchingLabels{
		constants.LabelAppName:      "matrixdb",
		constants.LabelAppInstance: cluster.Name,
	}); err != nil {
		return err
	}
	pods := podList.Items

	primaryPodName := cluster.Status.Primary
	if primaryPodName == "" {
		return nil
	}

	// 1. Upgrade replicas first
	for _, pod := range pods {
		if pod.Name == primaryPodName {
			continue
		}

		podImage := ""
		for _, c := range pod.Spec.Containers {
			if c.Name == "mysqld" {
				podImage = c.Image
				break
			}
		}

		if podImage != desiredImage {
			log.Info("Upgrading replica pod to new image", "pod", pod.Name, "desiredImage", desiredImage)
			r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "UpgradeReplica",
				"Upgrading replica %s to %s", pod.Name, desiredImage)
			// Delete replica pod so StatefulSet recreates it with new image
			_ = r.Delete(ctx, &pod)
			return nil // Wait for replica to recreate and become ready on next reconcile
		}
	}

	// 2. Check if all replicas are now upgraded and healthy
	allReplicasUpgraded := true
	var upgradedHealthyReplica string
	for _, pod := range pods {
		if pod.Name == primaryPodName {
			continue
		}
		podImage := ""
		for _, c := range pod.Spec.Containers {
			if c.Name == "mysqld" {
				podImage = c.Image
				break
			}
		}
		if podImage != desiredImage {
			allReplicasUpgraded = false
			break
		}
		// Check readiness
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				upgradedHealthyReplica = pod.Name
				break
			}
		}
	}

	if !allReplicasUpgraded || upgradedHealthyReplica == "" {
		log.Info("Waiting for all upgraded replicas to become ready and in-sync...")
		return nil
	}

	// 3. Gracefully switchover Primary to an upgraded replica
	primaryPodImage := ""
	for _, pod := range pods {
		if pod.Name == primaryPodName {
			for _, c := range pod.Spec.Containers {
				if c.Name == "mysqld" {
					primaryPodImage = c.Image
					break
				}
			}
			break
		}
	}

	if primaryPodImage != desiredImage {
		log.Info("All replicas upgraded. Executing switchover of Primary to upgraded replica",
			"oldPrimary", primaryPodName, "newPrimaryCandidate", upgradedHealthyReplica)
		r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "UpgradeSwitchover",
			"Switching primary from %s to upgraded replica %s", primaryPodName, upgradedHealthyReplica)

		creds, err := r.reconcileSecret(ctx, cluster)
		if err == nil {
			if err := r.gracefulSwitchover(ctx, cluster, creds, primaryPodName, upgradedHealthyReplica); err != nil {
				log.Error(err, "Failed graceful switchover to upgraded replica", "target", upgradedHealthyReplica)
				return err
			}
			return nil
		}
	}

	cluster.Status.Upgrading = false
	r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "UpgradeCompleted",
		"Replica-First rolling upgrade to %s completed successfully", desiredImage)
	return nil
}
