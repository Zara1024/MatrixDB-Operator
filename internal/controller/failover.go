package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/internal/mysql"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

func (r *MatrixDBClusterReconciler) handleFailover(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
	nodes map[string]*nodeRuntimeInfo,
) error {
	deadPrimaryName := cluster.Status.Primary
	r.Log.Info("Initiating automated GTID-based failover election", "failedPrimary", deadPrimaryName)

	// 1. Gather all alive replica candidates
	type candidate struct {
		name    string
		info    *nodeRuntimeInfo
		gtidSet *mysql.GTIDSet
	}

	var candidates []candidate
	for name, info := range nodes {
		if name == deadPrimaryName || !info.isAlive || info.isErrant {
			continue
		}
		isErrantFromStatus := false
		for _, prev := range cluster.Status.Nodes {
			if prev.PodName == name && prev.Errant {
				isErrantFromStatus = true
				break
			}
		}
		if isErrantFromStatus {
			r.Log.Info("Skipping quarantined errant node from election candidate pool", "node", name)
			continue
		}

		gtidStr := ""
		if info.replicaStatus != nil && info.replicaStatus.ExecutedGTID != "" {
			gtidStr = info.replicaStatus.ExecutedGTID
		} else if info.masterStatus != nil && info.masterStatus.ExecutedGTID != "" {
			gtidStr = info.masterStatus.ExecutedGTID
		} else if info.replicaStatus != nil {
			gtidStr = info.replicaStatus.RetrievedGTID
		}

		parsedGTID, _ := mysql.ParseGTIDSet(gtidStr)
		candidates = append(candidates, candidate{
			name:    name,
			info:    info,
			gtidSet: parsedGTID,
		})
	}

	if len(candidates) == 0 {
		cluster.Status.Phase = matrixdbv1alpha1.PhaseDegraded
		r.Recorder.Eventf(cluster, corev1.EventTypeWarning, "FailoverFailed", "No healthy replica candidates available for promotion")
		return fmt.Errorf("failover aborted: no healthy replica candidates alive in cluster %s", cluster.Name)
	}

	// 2. Select the candidate with the highest GTID transaction progress
	best := candidates[0]
	for i := 1; i < len(candidates); i++ {
		cmp := mysql.CompareGTIDSets(candidates[i].gtidSet, best.gtidSet)
		if cmp > 0 {
			best = candidates[i]
		}
	}

	r.Log.Info("Elected new primary based on maximum GTID transaction set",
		"newPrimary", best.name,
		"totalTransactions", best.gtidSet.TotalTransactions(),
	)

	// 3. Promote candidate to Primary
	if err := best.info.client.ResetReplicaAll(ctx); err != nil {
		r.Log.Error(err, "Warning during reset replica all on candidate", "candidate", best.name)
	}

	if err := best.info.client.SetReadOnly(ctx, false); err != nil {
		return fmt.Errorf("failed promoting %s (unable to unset read_only): %w", best.name, err)
	}

	// Ensure candidate has semi-sync master disabled during promotion to prevent DDL hangs
	_ = best.info.client.ConfigureLosslessSemiSync(ctx, false, cluster.Spec.SemiSyncTimeoutMS, 0)

	if err := best.info.client.ConfigureReplicationUser(ctx, constants.ReplicationUser, creds.ReplicaPassword); err != nil {
		r.Log.Error(err, "Warning configuring replication user on new primary", "candidate", best.name)
	}

	// Switch Pod labels so the Primary Service immediately targets the new Primary
	if err := r.setPodRoleLabel(ctx, cluster, &best.info.pod, constants.RolePrimary); err != nil {
		return fmt.Errorf("failed labeling promoted pod as primary: %w", err)
	}

	// 4. Re-attach all other alive replicas to the newly promoted primary
	newPrimaryHost := fmt.Sprintf("%s.%s-internal.%s.svc", best.name, cluster.Name, cluster.Namespace)
	syncedCount := 0
	for _, c := range candidates {
		if c.name == best.name {
			continue
		}
		r.Log.Info("Re-pointing replica to new primary", "replica", c.name, "newPrimary", newPrimaryHost)
		_ = c.info.client.ConfigureLosslessSemiSync(ctx, false, cluster.Spec.SemiSyncTimeoutMS, 0)
		if err := c.info.client.SetupAsReplica(ctx, newPrimaryHost, constants.MySQLPort, constants.ReplicationUser, creds.ReplicaPassword); err != nil {
			r.Log.Error(err, "Failed re-pointing replica to new primary", "replica", c.name)
		} else {
			syncedCount++
		}
		_ = r.setPodRoleLabel(ctx, cluster, &c.info.pod, constants.RoleReplica)
	}

	// 5. Configure Lossless Semi-Sync on new primary now that replicas are attached
	waitSlaveCount := int((cluster.Spec.Replicas - 1) / 2)
	if cluster.Spec.Replicas == 1 || syncedCount <= 0 {
		waitSlaveCount = 0
	} else if waitSlaveCount < 1 {
		waitSlaveCount = 1
	}
	_ = best.info.client.ConfigureLosslessSemiSync(ctx, true, cluster.Spec.SemiSyncTimeoutMS, waitSlaveCount)

	// 6. Update Status & Emit Event
	cluster.Status.Primary = best.name
	cluster.Status.Phase = matrixdbv1alpha1.PhaseHealthy
	r.Recorder.Eventf(cluster, corev1.EventTypeWarning, "FailoverExecuted",
		"Primary %s failed. Successfully promoted %s as new primary with zero data loss", deadPrimaryName, best.name)

	return nil
}
