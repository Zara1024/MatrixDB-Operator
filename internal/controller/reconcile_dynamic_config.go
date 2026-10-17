package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/internal/mysql"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

// reconcileDynamicConfig checks spec.mysqlConfig and hardware memory, and applies
// dynamic parameter changes via SET PERSIST to all active pods without restarting them.
func (r *MatrixDBClusterReconciler) reconcileDynamicConfig(
	ctx context.Context,
	cluster *matrixdbv1alpha1.MatrixDBCluster,
	creds *ClusterCredentials,
) error {
	desiredDynamic := make(map[string]string)

	// 1. Collect dynamic parameters from spec.mysqlConfig
	for k, v := range cluster.Spec.MysqlConfig {
		if mysql.IsDynamicVariable(k) {
			desiredDynamic[k] = v
		}
	}

	// 2. Hardware auto-tuning: if innodb_buffer_pool_size is not set, calculate from memory resource
	autoTunedBufferPool := r.getAutoTunedBufferPoolSize(cluster)
	if autoTunedBufferPool != "" {
		if _, exists := desiredDynamic["innodb_buffer_pool_size"]; !exists {
			if cluster.Spec.MysqlConfig == nil || cluster.Spec.MysqlConfig["innodb_buffer_pool_size"] == "" {
				desiredDynamic["innodb_buffer_pool_size"] = autoTunedBufferPool
			}
		}
	}

	if len(desiredDynamic) == 0 {
		return nil
	}

	// 3. Iterate through active cluster pods and persist dynamic parameters
	for i := 0; i < int(cluster.Spec.Replicas); i++ {
		podName := fmt.Sprintf("%s-%d", cluster.Name, i)
		podHost := fmt.Sprintf("%s.%s-internal.%s.svc", podName, cluster.Name, cluster.Namespace)

		client, err := mysql.NewClient(podHost, constants.MySQLPort, "root", creds.RootPassword)
		if err != nil {
			r.Log.V(1).Info("Unable to connect to pod for dynamic config, skipping", "pod", podName, "err", err)
			continue
		}

		pingCtx, pingCancel := context.WithTimeout(ctx, 2*time.Second)
		err = client.Ping(pingCtx)
		pingCancel()
		if err != nil {
			client.Close()
			continue
		}

		for varName, targetVal := range desiredDynamic {
			currentVal, err := client.GetGlobalVariable(ctx, varName)
			if err != nil {
				r.Log.V(1).Info("Failed getting variable from pod", "pod", podName, "var", varName, "err", err)
				continue
			}

			if mysql.AreValuesEquivalent(currentVal, targetVal) {
				continue
			}

			// Apply SET PERSIST
			if err := client.SetPersistVariable(ctx, varName, targetVal); err != nil {
				r.Log.Error(err, "Failed persisting dynamic parameter", "pod", podName, "var", varName, "targetVal", targetVal)
				continue
			}

			r.Log.Info("Hot reloaded dynamic MySQL parameter without restart",
				"pod", podName,
				"variable", varName,
				"oldValue", currentVal,
				"newValue", targetVal,
			)
			r.Recorder.Eventf(cluster, corev1.EventTypeNormal, "ParameterPersisted",
				"Dynamic parameter %s=%s persisted on pod %s without restart (was %s)", varName, targetVal, podName, currentVal)
		}

		client.Close()
	}

	return nil
}

func (r *MatrixDBClusterReconciler) getAutoTunedBufferPoolSize(cluster *matrixdbv1alpha1.MatrixDBCluster) string {
	if cluster.Spec.MysqlConfig != nil {
		if val, ok := cluster.Spec.MysqlConfig["innodb_buffer_pool_size"]; ok && val != "" {
			return val
		}
	}

	var memQuantity *resource.Quantity
	if req, ok := cluster.Spec.Resources.Requests[corev1.ResourceMemory]; ok && !req.IsZero() {
		memQuantity = &req
	} else if lim, ok := cluster.Spec.Resources.Limits[corev1.ResourceMemory]; ok && !lim.IsZero() {
		memQuantity = &lim
	}

	if memQuantity != nil {
		bytes := memQuantity.Value()
		if bytes > 0 {
			poolBytes := int64(float64(bytes) * 0.70)
			if poolBytes < 128*1024*1024 {
				poolBytes = 128 * 1024 * 1024
			}
			return fmt.Sprintf("%d", poolBytes)
		}
	}

	return ""
}
