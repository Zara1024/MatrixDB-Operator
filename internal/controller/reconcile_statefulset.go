package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

func (r *MatrixDBClusterReconciler) reconcileStatefulSet(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
	configMapName string,
) (*appsv1.StatefulSet, error) {
	stsName := cluster.Name
	secretName := creds.SecretName
	internalSvcName := fmt.Sprintf("%s-internal", cluster.Name)

	serverIDOffset := cluster.Spec.ServerIDOffset
	if serverIDOffset <= 0 {
		serverIDOffset = constants.DefaultServerIDOffset
	}

	storageSize, err := resource.ParseQuantity(cluster.Spec.Storage.Size)
	if err != nil {
		return nil, fmt.Errorf("invalid storage size format: %w", err)
	}

	initScript := fmt.Sprintf(`
set -e
ordinal=${HOSTNAME##*-}
server_id=$(( %d + ordinal + 1 ))
echo "[mysqld]" > /mnt/conf/server-id.cnf
echo "server-id=${server_id}" >> /mnt/conf/server-id.cnf
echo "Configured MySQL server-id to ${server_id}"
`, serverIDOffset)

	probeHandler := corev1.ProbeHandler{
		Exec: &corev1.ExecAction{
			Command: []string{
				"/bin/sh",
				"-c",
				"mysqladmin -h 127.0.0.1 -P 3306 -uroot -p\"$MYSQL_ROOT_PASSWORD\" ping || mysqladmin -uroot -p\"$MYSQL_ROOT_PASSWORD\" ping",
			},
		},
	}

	sts := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: stsName}, sts)
	if err != nil {
		if errors.IsNotFound(err) {
			newSts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      stsName,
					Namespace: cluster.Namespace,
					Labels: map[string]string{
						constants.LabelAppName:      "matrixdb",
						constants.LabelAppInstance: cluster.Name,
						constants.LabelClusterName:  cluster.Name,
						constants.LabelAppManagedBy: "matrixdb-operator",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					ServiceName: internalSvcName,
					Replicas:    &cluster.Spec.Replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							constants.LabelAppName:      "matrixdb",
							constants.LabelAppInstance: cluster.Name,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								constants.LabelAppName:      "matrixdb",
								constants.LabelAppInstance: cluster.Name,
								constants.LabelClusterName:  cluster.Name,
							},
						},
						Spec: corev1.PodSpec{
							Affinity:     cluster.Spec.Affinity,
							Tolerations:  cluster.Spec.Tolerations,
							NodeSelector: cluster.Spec.NodeSelector,
							TopologySpreadConstraints: func() []corev1.TopologySpreadConstraint {
								if len(cluster.Spec.TopologySpreadConstraints) > 0 {
									return cluster.Spec.TopologySpreadConstraints
								}
								return []corev1.TopologySpreadConstraint{
									{
										MaxSkew:           1,
										TopologyKey:       "kubernetes.io/hostname",
										WhenUnsatisfiable: corev1.ScheduleAnyway,
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												constants.LabelAppInstance: cluster.Name,
											},
										},
									},
								}
							}(),
							InitContainers: []corev1.Container{
								{
									Name:    "init-mysql",
									Image:   cluster.Spec.Image,
									Command: []string{"bash", "-c", initScript},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      constants.ServerIDVolumeName,
											MountPath: "/mnt/conf",
										},
									},
								},
							},
							Containers: r.buildContainers(cluster, secretName, probeHandler),
							Volumes: []corev1.Volume{
								{
									Name: constants.ConfigMapVolumeName,
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: configMapName,
											},
										},
									},
								},
								{
									Name: constants.ServerIDVolumeName,
									VolumeSource: corev1.VolumeSource{
										EmptyDir: &corev1.EmptyDirVolumeSource{},
									},
								},
							},
						},
					},
					VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: constants.DataVolumeName,
								Labels: map[string]string{
									constants.LabelAppName:      "matrixdb",
									constants.LabelAppInstance: cluster.Name,
								},
							},
							Spec: corev1.PersistentVolumeClaimSpec{
								AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
								StorageClassName: cluster.Spec.Storage.StorageClassName,
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceStorage: storageSize,
									},
								},
							},
						},
					},
				},
			}

			if err := controllerutil.SetControllerReference(cluster, newSts, r.Scheme); err != nil {
				return nil, fmt.Errorf("failed setting controller reference on statefulset: %w", err)
			}
			if err := r.Create(ctx, newSts); err != nil {
				return nil, fmt.Errorf("failed creating statefulset %s: %w", stsName, err)
			}
			r.Log.Info("Created MatrixDB StatefulSet", "statefulset", stsName)
			return newSts, nil
		}
		return nil, fmt.Errorf("failed to get statefulset %s: %w", stsName, err)
	}

	// StatefulSet exists: check if replica count or image need updates
	needUpdate := false
	if *sts.Spec.Replicas != cluster.Spec.Replicas {
		if cluster.Spec.Replicas < *sts.Spec.Replicas {
			// SCALE-DOWN DETECTED!
			r.Log.Info("Scale-down detected: checking if Primary needs graceful switchover",
				"currentReplicas", *sts.Spec.Replicas,
				"targetReplicas", cluster.Spec.Replicas,
				"currentPrimary", cluster.Status.Primary,
			)

			// Step A: Pre-Scale Switchover Check
			primaryOrdinal := getPodOrdinal(cluster.Status.Primary)
			if primaryOrdinal >= cluster.Spec.Replicas {
				// The current Primary is in the range of pods being deleted!
				r.Log.Info("Current Primary is scheduled for deletion during scale-down! Initiating Pre-Scale Graceful Switchover...",
					"primary", cluster.Status.Primary,
					"primaryOrdinal", primaryOrdinal,
					"targetReplicas", cluster.Spec.Replicas,
				)

				// Find best candidate among retained pods (ordinal 0 .. cluster.Spec.Replicas - 1)
				bestCandidate, err := r.findBestRetainedCandidate(ctx, cluster, creds, cluster.Spec.Replicas)
				if err != nil {
					return nil, fmt.Errorf("failed finding candidate for graceful switchover before scale-down: %w", err)
				}

				if err := r.gracefulSwitchover(ctx, cluster, creds, cluster.Status.Primary, bestCandidate); err != nil {
					return nil, fmt.Errorf("failed executing graceful switchover before scale-down: %w", err)
				}
			}

			// Step B: Stop replication on the pods that are about to be deleted
			for ord := cluster.Spec.Replicas; ord < *sts.Spec.Replicas; ord++ {
				termPodName := fmt.Sprintf("%s-%d", cluster.Name, ord)
				r.stopReplicaBeforeDelete(ctx, cluster, creds, termPodName)
			}

			// Step C: Adjust Semi-Sync on Primary (or disable if targetReplicas == 1)
			r.adjustSemiSyncForScaleDown(ctx, cluster, creds, cluster.Spec.Replicas)
		}

		r.Log.Info("Scaling StatefulSet replicas", "current", *sts.Spec.Replicas, "target", cluster.Spec.Replicas)
		sts.Spec.Replicas = &cluster.Spec.Replicas
		needUpdate = true
	}

	expectedContainers := r.buildContainers(cluster, secretName, probeHandler)
	containerChanged := false
	if len(sts.Spec.Template.Spec.Containers) != len(expectedContainers) {
		containerChanged = true
	} else {
		for i := range expectedContainers {
			if sts.Spec.Template.Spec.Containers[i].Image != expectedContainers[i].Image ||
				len(sts.Spec.Template.Spec.Containers[i].Args) != len(expectedContainers[i].Args) ||
				len(sts.Spec.Template.Spec.Containers[i].Env) != len(expectedContainers[i].Env) {
				containerChanged = true
				break
			}
		}
	}
	if containerChanged {
		r.Log.Info("Updating StatefulSet container specifications", "containers", len(expectedContainers))
		sts.Spec.Template.Spec.Containers = expectedContainers
		sts.Spec.Template.Spec.InitContainers[0].Image = cluster.Spec.Image
		needUpdate = true
	}

	if needUpdate {
		if err := r.Update(ctx, sts); err != nil {
			return nil, fmt.Errorf("failed updating statefulset %s: %w", stsName, err)
		}
	}

	return sts, nil
}

func (r *MatrixDBClusterReconciler) buildContainers(
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	secretName string,
	probeHandler corev1.ProbeHandler,
) []corev1.Container {
	containers := []corev1.Container{
		{
			Name:      "mysqld",
			Image:     cluster.Spec.Image,
			Resources: cluster.Spec.Resources,
			Ports: []corev1.ContainerPort{
				{
					Name:          "mysql",
					ContainerPort: constants.MySQLPort,
				},
			},
			Env: []corev1.EnvVar{
				{
					Name: "MYSQL_ROOT_PASSWORD",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: secretName,
							},
							Key: constants.SecretRootPasswordKey,
						},
					},
				},
			},
			StartupProbe: &corev1.Probe{
				ProbeHandler:        probeHandler,
				InitialDelaySeconds: 10,
				PeriodSeconds:       3,
				FailureThreshold:    60,
				TimeoutSeconds:      5,
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler:        probeHandler,
				InitialDelaySeconds: 5,
				PeriodSeconds:       5,
				FailureThreshold:    5,
				TimeoutSeconds:      5,
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler:        probeHandler,
				InitialDelaySeconds: 30,
				PeriodSeconds:       10,
				FailureThreshold:    5,
				TimeoutSeconds:      5,
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      constants.DataVolumeName,
					MountPath: "/var/lib/mysql",
				},
				{
					Name:      constants.ConfigMapVolumeName,
					MountPath: "/etc/mysql/conf.d/matrixdb.cnf",
					SubPath:   "matrixdb.cnf",
				},
				{
					Name:      constants.ServerIDVolumeName,
					MountPath: "/etc/mysql/conf.d/server-id.cnf",
					SubPath:   "server-id.cnf",
				},
			},
		},
	}

	if cluster.Spec.Metrics != nil && cluster.Spec.Metrics.Enabled {
		exporterImg := cluster.Spec.Metrics.Image
		if exporterImg == "" {
			exporterImg = "prom/mysqld-exporter:v0.15.1"
		}
		exporterPort := int32(9104)
		if cluster.Spec.Metrics.Port > 0 {
			exporterPort = cluster.Spec.Metrics.Port
		}

		containers = append(containers, corev1.Container{
			Name:  "mysqld-exporter",
			Image: exporterImg,
			Args: []string{
				"--mysqld.username=root",
				fmt.Sprintf("--mysqld.address=127.0.0.1:%d", constants.MySQLPort),
				fmt.Sprintf("--web.listen-address=:%d", exporterPort),
			},
			Ports: []corev1.ContainerPort{
				{
					Name:          "metrics",
					ContainerPort: exporterPort,
				},
			},
			Env: []corev1.EnvVar{
				{
					Name: "MYSQLD_EXPORTER_PASSWORD",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: secretName,
							},
							Key: constants.SecretRootPasswordKey,
						},
					},
				},
			},
			Resources: cluster.Spec.Metrics.Resources,
		})
	}

	return containers
}

