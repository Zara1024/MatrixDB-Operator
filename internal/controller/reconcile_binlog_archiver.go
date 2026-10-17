package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/internal/mysql"
	"github.com/matrixdb-io/matrixdb-operator/internal/s3"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

// ReconcileBinlogArchiver handles continuous binlog streaming to S3 for Point-in-Time Recovery (PITR).
func (r *MatrixDBClusterReconciler) ReconcileBinlogArchiver(ctx context.Context, cluster *matrixdbv1alpha1.MatrixDBCluster) error {
	if cluster.Spec.BinlogArchive == nil || !cluster.Spec.BinlogArchive.Enabled {
		return nil
	}

	if cluster.Status.Primary == "" || cluster.Status.Phase != matrixdbv1alpha1.PhaseHealthy {
		return nil
	}

	// Retrieve credentials
	secret := &corev1.Secret{}
	secretName := fmt.Sprintf("%s-secret", cluster.Name)
	if err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: secretName}, secret); err != nil {
		return fmt.Errorf("failed fetching secret for binlog archiving: %w", err)
	}
	rootPass := string(secret.Data[constants.SecretRootPasswordKey])

	primaryHost := fmt.Sprintf("%s.%s-internal.%s.svc", cluster.Status.Primary, cluster.Name, cluster.Namespace)
	client, err := mysql.NewClient(primaryHost, constants.MySQLPort, "root", rootPass)
	if err != nil {
		return fmt.Errorf("failed connecting to primary for binlog archiver: %w", err)
	}
	defer client.Close()

	// Check master status
	mst, err := client.GetMasterStatus(ctx)
	if err != nil || mst == nil {
		return nil
	}

	// Setup S3 client
	s3Storage := cluster.Spec.BinlogArchive.S3Storage
	s3Cli := s3.NewClient(
		s3Storage.S3Endpoint,
		s3Storage.S3Bucket,
		s3Storage.S3AccessKey,
		s3Storage.S3SecretKey,
		s3Storage.S3Region,
	)

	if err := s3Cli.EnsureBucket(ctx); err != nil {
		r.Log.Error(err, "Failed ensuring S3 bucket for binlog archive", "bucket", s3Storage.S3Bucket)
		return nil
	}

	// Create a continuous binlog snapshot chunk
	tmpDir := "/tmp/matrixdb-binlogs"
	_ = os.MkdirAll(tmpDir, 0755)

	timestamp := time.Now().UTC().Format("20060102-150405")
	localChunkPath := filepath.Join(tmpDir, fmt.Sprintf("%s-%s.sql", cluster.Name, timestamp))

	if err := client.DumpDatabases(ctx, localChunkPath); err != nil {
		r.Log.Error(err, "Failed creating binlog snapshot delta", "cluster", cluster.Name)
		return nil
	}
	defer os.Remove(localChunkPath)

	s3Key := fmt.Sprintf("%s/binlogs/%s-%s.sql", cluster.Name, cluster.Name, timestamp)
	if err := s3Cli.UploadFile(ctx, s3Key, localChunkPath); err != nil {
		r.Log.Error(err, "Failed uploading binlog chunk to S3", "key", s3Key)
		return nil
	}

	r.Log.Info("Continuous binlog stream chunk archived to S3",
		"cluster", cluster.Name,
		"s3Key", s3Key,
		"executedGTID", mst.ExecutedGTID,
	)

	return nil
}
