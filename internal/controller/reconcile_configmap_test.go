package controller

import (
	"strings"
	"testing"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
)

func TestRenderMyCnf(t *testing.T) {
	r := &MatrixDBClusterReconciler{}

	cluster := &matrixdbv1alpha1.MatrixDBCluster{
		Spec: matrixdbv1alpha1.MatrixDBClusterSpec{
			Replicas:          3,
			SemiSyncTimeoutMS: 5000000,
			MysqlConfig: map[string]string{
				"innodb_buffer_pool_size": "512M",
				"max_connections":         "800",
			},
		},
	}

	cnf := r.renderMyCnf(cluster)

	expectedDirectives := []string{
		"binlog_format = ROW",
		"gtid_mode = ON",
		"enforce_gtid_consistency = ON",
		"relay_log_recovery = OFF",
		"innodb_buffer_pool_size = 512M",
		"max_connections = 800",
	}

	for _, directive := range expectedDirectives {
		if !strings.Contains(cnf, directive) {
			t.Errorf("expected directive %q not found in rendered my.cnf:\n%s", directive, cnf)
		}
	}
}
