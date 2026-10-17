package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

const (
	defaultProxySQLImage = "proxysql/proxysql:2.5.5"
)

// reconcileProxySQL manages the ProxySQL deployment, service, and routing config.
func (r *MatrixDBClusterReconciler) reconcileProxySQL(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
) error {
	if cluster.Spec.Proxy == nil || !cluster.Spec.Proxy.Enabled {
		// Clean up ProxySQL resources if disabled
		r.cleanupProxySQL(ctx, cluster)
		return nil
	}

	configContent := r.renderProxySQLConfig(cluster, creds)
	hash := sha256.Sum256([]byte(configContent))
	configHash := hex.EncodeToString(hash[:])[:12]

	if err := r.reconcileProxyConfigMap(ctx, cluster, configContent); err != nil {
		return fmt.Errorf("failed reconciling proxysql configmap: %w", err)
	}

	if err := r.reconcileProxyService(ctx, cluster); err != nil {
		return fmt.Errorf("failed reconciling proxysql service: %w", err)
	}

	if err := r.reconcileProxyDeployment(ctx, cluster, configHash); err != nil {
		return fmt.Errorf("failed reconciling proxysql deployment: %w", err)
	}

	return nil
}

func (r *MatrixDBClusterReconciler) renderProxySQLConfig(
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
) string {
	var buf bytes.Buffer
	buf.WriteString(`datadir="/var/lib/proxysql"

admin_variables=
{
    admin_credentials="admin:admin;radmin:radmin"
    mysql_ifaces="0.0.0.0:6032"
}

mysql_variables=
{
    threads=4
    max_connections=2048
    default_query_delay=0
    default_query_timeout=36000000
    have_compress=true
    poll_timeout=2000
    interfaces="0.0.0.0:6033"
    default_schema="information_schema"
    stacksize=1048576
    server_version="8.0.34"
    connect_timeout_server=3000
}
`)

	// mysql_servers: Hostgroup 0 is Primary (writer), Hostgroup 1 is Replicas (readers)
	buf.WriteString("\nmysql_servers =\n(\n")
	primaryPod := cluster.Status.Primary
	if primaryPod == "" {
		primaryPod = fmt.Sprintf("%s-0", cluster.Name)
	}
	primaryFQDN := fmt.Sprintf("%s.%s-internal.%s.svc", primaryPod, cluster.Name, cluster.Namespace)

	// Primary in Hostgroup 0 (Writer)
	buf.WriteString(fmt.Sprintf(`    {
        address = "%s"
        port = %d
        hostgroup = 0
        max_connections = 1000
        weight = 1000
    }`, primaryFQDN, constants.MySQLPort))

	replicaCount := 0
	for i := 0; i < int(cluster.Spec.Replicas); i++ {
		podName := fmt.Sprintf("%s-%d", cluster.Name, i)
		if podName == primaryPod {
			continue
		}
		replicaCount++
		podFQDN := fmt.Sprintf("%s.%s-internal.%s.svc", podName, cluster.Name, cluster.Namespace)
		buf.WriteString(fmt.Sprintf(`,
    {
        address = "%s"
        port = %d
        hostgroup = 1
        max_connections = 1000
        weight = 100
    }`, podFQDN, constants.MySQLPort))
	}

	// If no read replicas, allow primary to also serve hostgroup 1
	if replicaCount == 0 {
		buf.WriteString(fmt.Sprintf(`,
    {
        address = "%s"
        port = %d
        hostgroup = 1
        max_connections = 1000
        weight = 100
    }`, primaryFQDN, constants.MySQLPort))
	}
	buf.WriteString("\n)\n")

	// mysql_users
	buf.WriteString(fmt.Sprintf(`
mysql_users =
(
    {
        username = "root"
        password = "%s"
        default_hostgroup = 0
        transaction_persistent = 1
    }
)
`, creds.RootPassword))

	// mysql_query_rules: Route SELECTs to hostgroup 1, SELECT ... FOR UPDATE & writes to hostgroup 0
	buf.WriteString(`
mysql_query_rules =
(
    {
        rule_id = 1
        active = 1
        match_pattern = "^SELECT .* FOR UPDATE"
        destination_hostgroup = 0
        apply = 1
    },
    {
        rule_id = 2
        active = 1
        match_pattern = "^SELECT "
        destination_hostgroup = 1
        apply = 1
    }
)
`)

	return buf.String()
}

func (r *MatrixDBClusterReconciler) reconcileProxyConfigMap(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	configContent string,
) error {
	cmName := fmt.Sprintf("%s-proxysql-config", cluster.Name)
	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: cmName}, cm)
	if err != nil {
		if errors.IsNotFound(err) {
			newCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: cluster.Namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name":       "proxysql",
						"app.kubernetes.io/instance":   cluster.Name,
						"app.kubernetes.io/component":  "proxy",
						"app.kubernetes.io/managed-by": "matrixdb-operator",
					},
				},
				Data: map[string]string{
					"proxysql.cnf": configContent,
				},
			}
			if err := controllerutil.SetControllerReference(cluster, newCM, r.Scheme); err != nil {
				return err
			}
			return r.Create(ctx, newCM)
		}
		return err
	}

	if cm.Data["proxysql.cnf"] != configContent {
		cm.Data["proxysql.cnf"] = configContent
		return r.Update(ctx, cm)
	}
	return nil
}

func (r *MatrixDBClusterReconciler) reconcileProxyService(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
) error {
	svcName := fmt.Sprintf("%s-proxysql", cluster.Name)
	svc := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: svcName}, svc)
	if err != nil {
		if errors.IsNotFound(err) {
			newSvc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      svcName,
					Namespace: cluster.Namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name":       "proxysql",
						"app.kubernetes.io/instance":   cluster.Name,
						"app.kubernetes.io/component":  "proxy",
						"app.kubernetes.io/managed-by": "matrixdb-operator",
					},
				},
				Spec: corev1.ServiceSpec{
					Type: corev1.ServiceTypeClusterIP,
					Ports: []corev1.ServicePort{
						{
							Name:       "mysql",
							Port:       6033,
							TargetPort: intstr.FromInt(6033),
						},
						{
							Name:       "admin",
							Port:       6032,
							TargetPort: intstr.FromInt(6032),
						},
					},
					Selector: map[string]string{
						"app.kubernetes.io/name":     "proxysql",
						"app.kubernetes.io/instance": cluster.Name,
					},
				},
			}
			if err := controllerutil.SetControllerReference(cluster, newSvc, r.Scheme); err != nil {
				return err
			}
			return r.Create(ctx, newSvc)
		}
		return err
	}
	return nil
}

func (r *MatrixDBClusterReconciler) reconcileProxyDeployment(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	configHash string,
) error {
	deployName := fmt.Sprintf("%s-proxysql", cluster.Name)
	cmName := fmt.Sprintf("%s-proxysql-config", cluster.Name)

	image := cluster.Spec.Proxy.Image
	if image == "" {
		image = defaultProxySQLImage
	}
	replicas := cluster.Spec.Proxy.Replicas
	if replicas <= 0 {
		replicas = 2
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       "proxysql",
		"app.kubernetes.io/instance":   cluster.Name,
		"app.kubernetes.io/component":  "proxy",
		"app.kubernetes.io/managed-by": "matrixdb-operator",
	}

	deploy := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: deployName}, deploy)
	if err != nil {
		if errors.IsNotFound(err) {
			newDeploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deployName,
					Namespace: cluster.Namespace,
					Labels:    labels,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name":     "proxysql",
							"app.kubernetes.io/instance": cluster.Name,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: labels,
							Annotations: map[string]string{
								"apps.matrixdb.io/config-hash": configHash,
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:    "proxysql",
									Image:   image,
									Command: []string{"proxysql"},
									Args:    []string{"-f", "-c", "/etc/proxysql.cnf", "--initial"},
									Ports: []corev1.ContainerPort{
										{Name: "mysql", ContainerPort: 6033},
										{Name: "admin", ContainerPort: 6032},
									},
									ReadinessProbe: &corev1.Probe{
										ProbeHandler: corev1.ProbeHandler{
											TCPSocket: &corev1.TCPSocketAction{
												Port: intstr.FromInt(6033),
											},
										},
										InitialDelaySeconds: 2,
										PeriodSeconds:       3,
									},
									LivenessProbe: &corev1.Probe{
										ProbeHandler: corev1.ProbeHandler{
											TCPSocket: &corev1.TCPSocketAction{
												Port: intstr.FromInt(6033),
											},
										},
										InitialDelaySeconds: 5,
										PeriodSeconds:       10,
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "config",
											MountPath: "/etc/proxysql.cnf",
											SubPath:   "proxysql.cnf",
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "config",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: cmName,
											},
										},
									},
								},
							},
						},
					},
				},
			}
			if err := controllerutil.SetControllerReference(cluster, newDeploy, r.Scheme); err != nil {
				return err
			}
			return r.Create(ctx, newDeploy)
		}
		return err
	}

	// Update if replicas, image, or config hash changed
	needUpdate := false
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != replicas {
		deploy.Spec.Replicas = &replicas
		needUpdate = true
	}
	if len(deploy.Spec.Template.Spec.Containers) > 0 && deploy.Spec.Template.Spec.Containers[0].Image != image {
		deploy.Spec.Template.Spec.Containers[0].Image = image
		needUpdate = true
	}
	if len(deploy.Spec.Template.Spec.Containers) > 0 && len(deploy.Spec.Template.Spec.Containers[0].Command) == 0 {
		deploy.Spec.Template.Spec.Containers[0].Command = []string{"proxysql"}
		needUpdate = true
	}
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = make(map[string]string)
	}
	if deploy.Spec.Template.Annotations["apps.matrixdb.io/config-hash"] != configHash {
		deploy.Spec.Template.Annotations["apps.matrixdb.io/config-hash"] = configHash
		needUpdate = true
	}

	if needUpdate {
		return r.Update(ctx, deploy)
	}
	return nil
}

func (r *MatrixDBClusterReconciler) cleanupProxySQL(ctx context.Context, cluster *matrixdbv1alpha1.MatrixDBCluster) {
	name := fmt.Sprintf("%s-proxysql", cluster.Name)
	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: name}, deploy); err == nil {
		_ = r.Delete(ctx, deploy)
	}
	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: name}, svc); err == nil {
		_ = r.Delete(ctx, svc)
	}
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: fmt.Sprintf("%s-proxysql-config", cluster.Name)}, cm); err == nil {
		_ = r.Delete(ctx, cm)
	}
}
