package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/internal/mysql"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

// ReconcileDBADoctor performs autonomous database diagnostics, MDL deadlock killer, and lag optimization
func (r *MatrixDBClusterReconciler) ReconcileDBADoctor(ctx context.Context, cluster *matrixdbv1alpha1.MatrixDBCluster) error {
	if cluster.Spec.Doctor == nil || !cluster.Spec.Doctor.Enabled {
		return nil
	}

	log := r.Log.WithValues("cluster", cluster.Name, "stage", "dba-doctor")
	log.V(1).Info("Running DBA Doctor diagnostic check")

	secret := &corev1.Secret{}
	secretName := fmt.Sprintf("%s-secret", cluster.Name)
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: secretName}, secret); err != nil {
		return nil
	}
	rootPass := string(secret.Data[constants.SecretRootPasswordKey])

	// 1. Inspect Primary for blocking MDL locks
	if cluster.Status.Primary != "" {
		primaryHost := fmt.Sprintf("%s.%s-internal.%s.svc", cluster.Status.Primary, cluster.Name, cluster.Namespace)
		client, err := mysql.NewClient(primaryHost, constants.MySQLPort, "root", rootPass)
		if err == nil {
			defer client.Close()
			r.killLongMDLLocks(ctx, cluster, client)
		}
	}

	// 2. Inspect Replicas for replication lag and optimize parallel workers
	for _, node := range cluster.Status.Nodes {
		if node.Role == matrixdbv1alpha1.NodeRoleReplica && node.Ready {
			replicaHost := fmt.Sprintf("%s.%s-internal.%s.svc", node.PodName, cluster.Name, cluster.Namespace)
			rClient, err := mysql.NewClient(replicaHost, constants.MySQLPort, "root", rootPass)
			if err == nil {
				defer rClient.Close()
				r.optimizeReplicationLag(ctx, cluster, node.PodName, rClient)
			}
		}
	}

	return nil
}

// killLongMDLLocks identifies transactions waiting for metadata locks and kills blocking idle transactions
func (r *MatrixDBClusterReconciler) killLongMDLLocks(ctx context.Context, cluster *matrixdbv1alpha1.MatrixDBCluster, client *mysql.Client) {
	threshold := int32(10)
	if cluster.Spec.Doctor != nil && cluster.Spec.Doctor.KillLongMDLSeconds > 0 {
		threshold = cluster.Spec.Doctor.KillLongMDLSeconds
	}

	query := `
		SELECT ID, USER, HOST, DB, COMMAND, TIME, STATE, INFO 
		FROM information_schema.processlist 
		WHERE COMMAND != 'Binlog Dump' AND COMMAND != 'Binlog Dump GTID'
	`
	rows, err := client.QueryContext(ctx, query)
	if err != nil {
		return
	}
	defer rows.Close()

	type proc struct {
		id      int64
		user    string
		host    string
		db      string
		command string
		timeSec int64
		state   string
		info    string
	}

	var procs []proc
	hasMDLWait := false

	for rows.Next() {
		var p proc
		var db, state, info *string
		if err := rows.Scan(&p.id, &p.user, &p.host, &db, &p.command, &p.timeSec, &state, &info); err != nil {
			continue
		}
		if db != nil {
			p.db = *db
		}
		if state != nil {
			p.state = *state
		}
		if info != nil {
			p.info = *info
		}
		if strings.Contains(strings.ToLower(p.state), "waiting for table metadata lock") {
			hasMDLWait = true
		}
		procs = append(procs, p)
	}

	if hasMDLWait {
		for _, p := range procs {
			// If a thread is sleeping or idle in transaction longer than threshold, kill it to release MDL lock
			if (p.command == "Sleep" || strings.Contains(p.state, "waiting")) && p.timeSec >= int64(threshold) {
				killSQL := fmt.Sprintf("KILL %d", p.id)
				if _, err := client.ExecContext(ctx, killSQL); err == nil {
					r.Log.Info("DBA Doctor killed blocking transaction holding MDL", "threadId", p.id, "idleTime", p.timeSec, "user", p.user)
					r.Recorder.Eventf(cluster, corev1.EventTypeWarning, "MDLLockKilled",
						"DBA Doctor killed blocking transaction %d (idle %ds) to unblock MDL lock", p.id, p.timeSec)
				}
			}
		}
	}
}

// optimizeReplicationLag checks replica lag and adjusts parallel execution workers dynamically
func (r *MatrixDBClusterReconciler) optimizeReplicationLag(ctx context.Context, cluster *matrixdbv1alpha1.MatrixDBCluster, podName string, client *mysql.Client) {
	rep, err := client.GetReplicaStatus(ctx)
	if err != nil || rep == nil {
		return
	}

	threshold := int32(10)
	if cluster.Spec.Doctor != nil && cluster.Spec.Doctor.MaxReplicationLagSeconds > 0 {
		threshold = cluster.Spec.Doctor.MaxReplicationLagSeconds
	}

	if rep.SecondsBehindSource != nil && *rep.SecondsBehindSource > int64(threshold) {
		r.Log.Info("DBA Doctor detected replication lag, tuning parallel workers", "pod", podName, "lagSec", *rep.SecondsBehindSource)
		// Dynamically increase replica parallel workers to accelerate replay
		_, _ = client.ExecContext(ctx, "SET GLOBAL replica_parallel_workers = 16;")
		r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "ParallelWorkersTuned",
			"DBA Doctor tuned replica_parallel_workers=16 on %s (lag: %ds)", podName, *rep.SecondsBehindSource)
	}
}
