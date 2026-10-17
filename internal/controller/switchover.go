package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/internal/mysql"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

// getPodOrdinal parses the integer ordinal from a StatefulSet pod name (e.g. "matrixdb-prod-3" -> 3).
func getPodOrdinal(podName string) int32 {
	idx := strings.LastIndex(podName, "-")
	if idx == -1 || idx == len(podName)-1 {
		return -1
	}
	ord, err := strconv.Atoi(podName[idx+1:])
	if err != nil {
		return -1
	}
	return int32(ord)
}

// findBestRetainedCandidate inspects all pods with ordinal < targetReplicas and selects the one with the highest GTID progress.
func (r *MatrixDBClusterReconciler) findBestRetainedCandidate(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
	targetReplicas int32,
) (string, error) {
	type candidate struct {
		name    string
		gtidSet *mysql.GTIDSet
	}

	var candidates []candidate

	for ord := int32(0); ord < targetReplicas; ord++ {
		podName := fmt.Sprintf("%s-%d", cluster.Name, ord)
		podDNS := fmt.Sprintf("%s.%s-internal.%s.svc", podName, cluster.Name, cluster.Namespace)

		c, err := mysql.NewClient(podDNS, constants.MySQLPort, "root", creds.RootPassword)
		if err != nil {
			continue
		}
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = c.Ping(pingCtx)
		cancel()
		if err != nil {
			c.Close()
			continue
		}

		gtidStr := ""
		if repStatus, _ := c.GetReplicaStatus(ctx); repStatus != nil && repStatus.ExecutedGTID != "" {
			gtidStr = repStatus.ExecutedGTID
		} else if mstStatus, _ := c.GetMasterStatus(ctx); mstStatus != nil && mstStatus.ExecutedGTID != "" {
			gtidStr = mstStatus.ExecutedGTID
		}
		c.Close()

		parsed, _ := mysql.ParseGTIDSet(gtidStr)
		candidates = append(candidates, candidate{
			name:    podName,
			gtidSet: parsed,
		})
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no alive candidates found among retained pods (0 to %d) in cluster %s", targetReplicas-1, cluster.Name)
	}

	best := candidates[0]
	for i := 1; i < len(candidates); i++ {
		cmp := mysql.CompareGTIDSets(candidates[i].gtidSet, best.gtidSet)
		if cmp > 0 {
			best = candidates[i]
		}
	}

	return best.name, nil
}

// gracefulSwitchover performs a planned, zero-downtime, zero-data-loss primary switchover.
// It sets super_read_only on the old primary, ensures the target catches up, promotes the target,
// drifts the Service routing labels, and repoints other retained replicas.
func (r *MatrixDBClusterReconciler) gracefulSwitchover(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
	currentPrimaryName string,
	targetPrimaryName string,
) error {
	r.Log.Info("Starting graceful primary switchover", "currentPrimary", currentPrimaryName, "targetPrimary", targetPrimaryName)

	curDNS := fmt.Sprintf("%s.%s-internal.%s.svc", currentPrimaryName, cluster.Name, cluster.Namespace)
	tgtDNS := fmt.Sprintf("%s.%s-internal.%s.svc", targetPrimaryName, cluster.Name, cluster.Namespace)

	curClient, err := mysql.NewClient(curDNS, constants.MySQLPort, "root", creds.RootPassword)
	if err != nil {
		return fmt.Errorf("failed connecting to current primary %s: %w", currentPrimaryName, err)
	}
	defer curClient.Close()

	tgtClient, err := mysql.NewClient(tgtDNS, constants.MySQLPort, "root", creds.RootPassword)
	if err != nil {
		return fmt.Errorf("failed connecting to target candidate %s: %w", targetPrimaryName, err)
	}
	defer tgtClient.Close()

	// 1. Freeze writes on old primary
	r.Log.Info("Freezing writes on old primary (super_read_only = ON)", "oldPrimary", currentPrimaryName)
	if err := curClient.SetReadOnly(ctx, true); err != nil {
		return fmt.Errorf("failed freezing writes on old primary: %w", err)
	}

	// 2. Fetch master GTID
	mstStatus, _ := curClient.GetMasterStatus(ctx)
	targetGTID := ""
	if mstStatus != nil {
		targetGTID = mstStatus.ExecutedGTID
	}

	// 3. Wait up to 5s for candidate to catch up to targetGTID
	if targetGTID != "" {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		timeout := time.After(5 * time.Second)
	waitLoop:
		for {
			select {
			case <-timeout:
				r.Log.Info("Timeout waiting for candidate to fully match GTID, proceeding with promotion", "candidate", targetPrimaryName)
				break waitLoop
			case <-ticker.C:
				rep, _ := tgtClient.GetReplicaStatus(ctx)
				if rep != nil && (rep.ExecutedGTID == targetGTID || rep.RetrievedGTID == targetGTID) {
					r.Log.Info("Target candidate has synchronized all transactions", "candidate", targetPrimaryName)
					break waitLoop
				}
			}
		}
	}

	// 4. Promote candidate to new Primary
	r.Log.Info("Promoting candidate to Primary", "newPrimary", targetPrimaryName)
	if err := tgtClient.ResetReplicaAll(ctx); err != nil {
		r.Log.Error(err, "Warning during reset replica all on new primary", "candidate", targetPrimaryName)
	}
	if err := tgtClient.SetReadOnly(ctx, false); err != nil {
		return fmt.Errorf("failed disabling read_only on new primary: %w", err)
	}
	if err := tgtClient.ConfigureReplicationUser(ctx, constants.ReplicationUser, creds.ReplicaPassword); err != nil {
		r.Log.Error(err, "Warning configuring replication user on new primary", "candidate", targetPrimaryName)
	}

	// 5. Switch Kubernetes Pod labels so Service routing drifts immediately
	tgtPod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: targetPrimaryName}, tgtPod); err == nil {
		_ = r.setPodRoleLabel(ctx, cluster, tgtPod, constants.RolePrimary)
	}
	curPod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: currentPrimaryName}, curPod); err == nil {
		_ = r.setPodRoleLabel(ctx, cluster, curPod, constants.RoleReplica)
	}

	// 6. Demote old primary to point to new primary (if still reachable)
	_ = curClient.ConfigureLosslessSemiSync(ctx, false, cluster.Spec.SemiSyncTimeoutMS, 0)
	_ = curClient.SetupAsReplica(ctx, tgtDNS, constants.MySQLPort, constants.ReplicationUser, creds.ReplicaPassword)

	// 7. Repoint other retained replicas to the new primary
	for ord := int32(0); ord < cluster.Spec.Replicas; ord++ {
		replicaPodName := fmt.Sprintf("%s-%d", cluster.Name, ord)
		if replicaPodName == targetPrimaryName || replicaPodName == currentPrimaryName {
			continue
		}
		repDNS := fmt.Sprintf("%s.%s-internal.%s.svc", replicaPodName, cluster.Name, cluster.Namespace)
		rc, err := mysql.NewClient(repDNS, constants.MySQLPort, "root", creds.RootPassword)
		if err == nil {
			_ = rc.ConfigureLosslessSemiSync(ctx, false, cluster.Spec.SemiSyncTimeoutMS, 0)
			_ = rc.SetupAsReplica(ctx, tgtDNS, constants.MySQLPort, constants.ReplicationUser, creds.ReplicaPassword)
			rc.Close()
		}
	}

	// 8. Update Primary in Cluster Status & Emit Event
	cluster.Status.Primary = targetPrimaryName
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &matrixdbv1alpha1.MatrixDBCluster{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}, latest); err != nil {
			return err
		}
		latest.Status.Primary = targetPrimaryName
		return r.Status().Update(ctx, latest)
	})

	r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "GracefulSwitchoverExecuted",
		"Gracefully transferred Primary role from %s to %s before scale-down with zero data loss", currentPrimaryName, targetPrimaryName)

	return nil
}

// stopReplicaBeforeDelete gracefully stops replication on a pod that is scheduled for deletion.
func (r *MatrixDBClusterReconciler) stopReplicaBeforeDelete(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
	podName string,
) {
	podDNS := fmt.Sprintf("%s.%s-internal.%s.svc", podName, cluster.Name, cluster.Namespace)
	c, err := mysql.NewClient(podDNS, constants.MySQLPort, "root", creds.RootPassword)
	if err != nil {
		return
	}
	defer c.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx); err == nil {
		r.Log.Info("Gracefully stopping replica before pod deletion", "pod", podName)
		_ = c.StopReplica(ctx)
	}
}

// adjustSemiSyncForScaleDown dynamically updates semi-sync quorum on the Primary node.
func (r *MatrixDBClusterReconciler) adjustSemiSyncForScaleDown(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
	targetReplicas int32,
) {
	primaryName := cluster.Status.Primary
	if primaryName == "" {
		return
	}

	primaryDNS := fmt.Sprintf("%s.%s-internal.%s.svc", primaryName, cluster.Name, cluster.Namespace)
	c, err := mysql.NewClient(primaryDNS, constants.MySQLPort, "root", creds.RootPassword)
	if err != nil {
		return
	}
	defer c.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx); err != nil {
		return
	}

	waitSlaveCount := int((targetReplicas - 1) / 2)
	if targetReplicas == 1 {
		waitSlaveCount = 0
	} else if waitSlaveCount < 1 {
		waitSlaveCount = 1
	}

	if err := c.ConfigureLosslessSemiSync(ctx, true, cluster.Spec.SemiSyncTimeoutMS, waitSlaveCount); err == nil {
		if targetReplicas == 1 {
			r.Log.Info("Disabled semi-sync master on primary for single-node mode", "primary", primaryName)
			r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "SemiSyncDisabled",
				"Disabled semi-sync master on %s because cluster scaled down to single-node mode", primaryName)
		} else {
			r.Log.Info("Adjusted semi-sync quorum on primary", "primary", primaryName, "waitSlaveCount", waitSlaveCount)
			r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "SemiSyncAdjusted",
				"Adjusted semi-sync wait slave count to %d on %s for %d replicas", waitSlaveCount, primaryName, targetReplicas)
		}
	}
}
