package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/internal/mysql"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

type nodeRuntimeInfo struct {
	pod           corev1.Pod
	client        *mysql.Client
	isAlive       bool
	isReadOnly    bool
	isErrant      bool
	errantGTID    string
	replicaStatus *mysql.ReplicaStatus
	masterStatus  *mysql.MasterStatus
}

func (r *MatrixDBClusterReconciler) reconcileTopology(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
) error {
	// List all pods belonging to this cluster
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels{
			constants.LabelAppName:      "matrixdb",
			constants.LabelAppInstance: cluster.Name,
		},
	}
	if err := r.List(ctx, podList, listOpts...); err != nil {
		return fmt.Errorf("failed listing cluster pods: %w", err)
	}

	if len(podList.Items) == 0 {
		r.Log.Info("No Pods ready yet, waiting for StatefulSet to spin up instances")
		return nil
	}

	// Inspect all pods and establish connections
	nodeMap := make(map[string]*nodeRuntimeInfo)
	var nodeStatuses []matrixdbv1alpha1.NodeStatus
	var readyCount int32
	var syncedCount int32

	for _, pod := range podList.Items {
		if pod.DeletionTimestamp != nil {
			// Skip pods being terminated by StatefulSet scale-down
			continue
		}
		info := &nodeRuntimeInfo{pod: pod}
		podDNS := fmt.Sprintf("%s.%s-internal.%s.svc", pod.Name, cluster.Name, cluster.Namespace)

		// Create client
		c, err := mysql.NewClient(podDNS, constants.MySQLPort, "root", creds.RootPassword)
		if err == nil {
			info.client = c
			defer c.Close()

			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = c.Ping(pingCtx)
			cancel()

			if err == nil {
				info.isAlive = true
				readyCount++

				// Check read_only
				isRO, _ := c.IsReadOnly(ctx)
				info.isReadOnly = isRO

				// Check replica status
				repStatus, _ := c.GetReplicaStatus(ctx)
				info.replicaStatus = repStatus
				if repStatus != nil && repStatus.IORunning && repStatus.SQLRunning {
					syncedCount++
				}

				// Check master status
				mstStatus, _ := c.GetMasterStatus(ctx)
				info.masterStatus = mstStatus
			}
		}



		nodeMap[pod.Name] = info

		// Build NodeStatus for CRD
		role := matrixdbv1alpha1.NodeRoleReplica
		if pod.Labels[constants.LabelNodeRole] == constants.RolePrimary {
			role = matrixdbv1alpha1.NodeRolePrimary
		}

		ns := matrixdbv1alpha1.NodeStatus{
			PodName:  pod.Name,
			Role:     role,
			Ready:    info.isAlive,
			ReadOnly: info.isReadOnly,
		}
		if info.masterStatus != nil {
			ns.ExecutedGTID = info.masterStatus.ExecutedGTID
		}
		if info.replicaStatus != nil {
			ns.RetrievedGTID = info.replicaStatus.RetrievedGTID
			ns.IORunning = info.replicaStatus.IORunning
			ns.SQLRunning = info.replicaStatus.SQLRunning
		}
		nodeStatuses = append(nodeStatuses, ns)
	}

	cluster.Status.ReadyReplicas = readyCount
	cluster.Status.SyncedReplicas = syncedCount
	cluster.Status.Nodes = nodeStatuses

	// Initial Bootstrap Phase
	if cluster.Status.Primary == "" {
		pod0Name := fmt.Sprintf("%s-0", cluster.Name)
		pod0Info, exists := nodeMap[pod0Name]
		if !exists || !pod0Info.isAlive {
			r.Log.Info("Waiting for primary pod-0 to become ready for initial bootstrap", "pod", pod0Name)
			cluster.Status.Phase = matrixdbv1alpha1.PhaseInitializing
			return nil
		}

		r.Log.Info("Starting initial cluster bootstrap on pod-0", "pod", pod0Name)
		return r.bootstrapCluster(ctx, cluster, creds, nodeMap)
	}

	// Cluster already bootstrapped: verify health of primary
	primaryInfo, primaryExists := nodeMap[cluster.Status.Primary]
	if !primaryExists || !primaryInfo.isAlive {
		r.Log.Info("Detected Primary failure! Initiating automatic failover...", "deadPrimary", cluster.Status.Primary)
		cluster.Status.Phase = matrixdbv1alpha1.PhaseFailover
		return r.handleFailover(ctx, cluster, creds, nodeMap)
	}

	// Primary is healthy: check all replicas for errant transactions and quarantine any deviant nodes
	primaryGTID := ""
	if primaryInfo.masterStatus != nil {
		primaryGTID = primaryInfo.masterStatus.ExecutedGTID
	}
	var errantCount int32
	if primaryGTID != "" {
		for name, info := range nodeMap {
			if name == cluster.Status.Primary || !info.isAlive || info.client == nil {
				continue
			}
			// Only check errant GTID on replicas that are actively replicating
			if info.replicaStatus == nil || !info.replicaStatus.IORunning {
				continue
			}
			errant, err := info.client.GetErrantGTID(ctx, primaryGTID)
			if err == nil && errant != "" {
				info.isErrant = true
				info.errantGTID = errant
				errantCount++
				r.Log.Info("ALERT: Detected errant GTID on replica! Quarantining node from replica service and failover candidates...",
					"replica", name,
					"errantGTID", errant,
				)
				r.Recorder.Eventf(cluster, corev1.EventTypeWarning, "ErrantTransactionDetected",
					"Replica %s has errant transactions: %s. Quarantining node from replica service and failover candidate pool", name, errant)
				_ = r.stripPodRoleLabel(ctx, &info.pod)
			}
		}
	}
	cluster.Status.ErrantReplicas = errantCount

	// Re-build NodeStatus with Errant metadata
	for i, ns := range nodeStatuses {
		if info, ok := nodeMap[ns.PodName]; ok && info.isErrant {
			nodeStatuses[i].Errant = true
			nodeStatuses[i].ErrantGTID = info.errantGTID
			nodeStatuses[i].Role = matrixdbv1alpha1.NodeRoleUnknown
		}
	}
	cluster.Status.Nodes = nodeStatuses

	cluster.Status.Phase = matrixdbv1alpha1.PhaseHealthy
	return r.healReplicationTopology(ctx, cluster, creds, nodeMap)
}

func (r *MatrixDBClusterReconciler) bootstrapCluster(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
	nodes map[string]*nodeRuntimeInfo,
) error {
	pod0Name := fmt.Sprintf("%s-0", cluster.Name)
	pod0 := nodes[pod0Name]

	// 1. Configure Primary (pod-0)
	if err := pod0.client.ConfigureReplicationUser(ctx, constants.ReplicationUser, creds.ReplicaPassword); err != nil {
		return fmt.Errorf("failed configuring replication user on pod-0: %w", err)
	}
	if err := pod0.client.SetReadOnly(ctx, false); err != nil {
		return fmt.Errorf("failed disabling read_only on primary: %w", err)
	}

	// Label pod-0 as primary
	if err := r.setPodRoleLabel(ctx, cluster, &pod0.pod, constants.RolePrimary); err != nil {
		return err
	}

	// 2. Attach all other alive nodes as replicas
	primaryHost := fmt.Sprintf("%s.%s-internal.%s.svc", pod0Name, cluster.Name, cluster.Namespace)
	for name, info := range nodes {
		if name == pod0Name || !info.isAlive {
			continue
		}
		_ = info.client.ConfigureLosslessSemiSync(ctx, false, cluster.Spec.SemiSyncTimeoutMS, 0)
		if err := info.client.SetupAsReplica(ctx, primaryHost, constants.MySQLPort, constants.ReplicationUser, creds.ReplicaPassword); err != nil {
			r.Log.Error(err, "Failed attaching node as replica during bootstrap", "node", name)
			continue
		}
		_ = r.setPodRoleLabel(ctx, cluster, &info.pod, constants.RoleReplica)
	}

	// 3. Configure Lossless Semi-Sync on Primary now that replicas are attached
	attachedReplicas := 0
	for name, info := range nodes {
		if name != pod0Name && info.isAlive {
			attachedReplicas++
		}
	}
	waitSlaveCount := int((cluster.Spec.Replicas - 1) / 2)
	if cluster.Spec.Replicas == 1 || attachedReplicas == 0 {
		waitSlaveCount = 0
	} else if waitSlaveCount < 1 {
		waitSlaveCount = 1
	}
	_ = pod0.client.ConfigureLosslessSemiSync(ctx, true, cluster.Spec.SemiSyncTimeoutMS, waitSlaveCount)

	cluster.Status.Primary = pod0Name
	cluster.Status.Phase = matrixdbv1alpha1.PhaseHealthy
	r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "ClusterBootstrapped", "Cluster successfully bootstrapped with primary %s", pod0Name)
	return nil
}

func (r *MatrixDBClusterReconciler) healReplicationTopology(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
	nodes map[string]*nodeRuntimeInfo,
) error {
	primaryName := cluster.Status.Primary
	primary, primaryExists := nodes[primaryName]
	if !primaryExists || !primary.isAlive {
		return nil
	}

	// Ensure Primary has read_only = OFF, replication user, and dynamic semi-sync quorum
	if primary.isReadOnly {
		_ = primary.client.SetReadOnly(ctx, false)
	}
	_ = primary.client.ConfigureReplicationUser(ctx, constants.ReplicationUser, creds.ReplicaPassword)
	_ = r.setPodRoleLabel(ctx, cluster, &primary.pod, constants.RolePrimary)

	aliveReplicas := 0
	for name, info := range nodes {
		if name != primaryName && info.isAlive && !info.isErrant {
			aliveReplicas++
		}
	}

	waitSlaveCount := int((cluster.Spec.Replicas - 1) / 2)
	if cluster.Spec.Replicas == 1 || aliveReplicas == 0 {
		waitSlaveCount = 0
	} else if waitSlaveCount < 1 {
		waitSlaveCount = 1
	}
	_ = primary.client.ConfigureLosslessSemiSync(ctx, true, cluster.Spec.SemiSyncTimeoutMS, waitSlaveCount)

	primaryHost := fmt.Sprintf("%s.%s-internal.%s.svc", primaryName, cluster.Name, cluster.Namespace)

	// Check and heal replicas
	for name, info := range nodes {
		if name == primaryName || !info.isAlive || info.isErrant {
			continue
		}

		_ = r.setPodRoleLabel(ctx, cluster, &info.pod, constants.RoleReplica)

		// Check if replica is correctly pointed and actively replicating
		rep := info.replicaStatus
		isHealthy := rep != nil && rep.IORunning && rep.SQLRunning &&
			(rep.SourceHost == primaryHost || strings.HasPrefix(rep.SourceHost, primaryName))

		if !isHealthy {
			r.Log.Info("Healing desynchronized replica", "node", name, "primary", primaryHost)
			_ = info.client.ConfigureLosslessSemiSync(ctx, false, cluster.Spec.SemiSyncTimeoutMS, 0)
			if err := info.client.SetupAsReplica(ctx, primaryHost, constants.MySQLPort, constants.ReplicationUser, creds.ReplicaPassword); err != nil {
				r.Log.Error(err, "Failed healing replica", "node", name)
			}
		}
	}
	return nil
}

func (r *MatrixDBClusterReconciler) setPodRoleLabel(ctx context.Context, cluster *matrixdbv1alpha1.MatrixDBCluster, pod *corev1.Pod, role string) error {
	needUpdate := false
	podCopy := pod.DeepCopy()
	if podCopy.Labels == nil {
		podCopy.Labels = make(map[string]string)
	}
	if podCopy.Labels[constants.LabelNodeRole] != role {
		podCopy.Labels[constants.LabelNodeRole] = role
		needUpdate = true
	}

	expectedRoleType := "oltp"
	if cluster.Spec.AnalyticalReplicas > 0 {
		var ordinal int
		parts := strings.Split(pod.Name, "-")
		if len(parts) > 0 {
			if ord, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
				ordinal = ord
			}
		}
		if int32(ordinal) >= (cluster.Spec.Replicas - cluster.Spec.AnalyticalReplicas) {
			expectedRoleType = "analytics"
		}
	}
	if podCopy.Labels["matrixdb.io/role-type"] != expectedRoleType {
		podCopy.Labels["matrixdb.io/role-type"] = expectedRoleType
		needUpdate = true
	}

	if !needUpdate {
		return nil
	}
	return r.Update(ctx, podCopy)
}

func (r *MatrixDBClusterReconciler) stripPodRoleLabel(ctx context.Context, pod *corev1.Pod) error {
	if pod.Labels == nil {
		return nil
	}
	if _, ok := pod.Labels[constants.LabelNodeRole]; !ok {
		return nil
	}
	podCopy := pod.DeepCopy()
	delete(podCopy.Labels, constants.LabelNodeRole)
	return r.Update(ctx, podCopy)
}

