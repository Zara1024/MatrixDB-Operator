package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupPhase represents the lifecycle state of a MatrixDBBackup.
type BackupPhase string

const (
	BackupPhasePending   BackupPhase = "Pending"
	BackupPhaseRunning   BackupPhase = "Running"
	BackupPhaseCompleted BackupPhase = "Completed"
	BackupPhaseFailed    BackupPhase = "Failed"
)

// BackupStorageConfig defines where the backup is stored.
type BackupStorageConfig struct {
	// LocalPath defines the host/container path to save the backup snapshot.
	// Defaults to /var/backups/matrixdb.
	// +optional
	LocalPath string `json:"localPath,omitempty"`

	// S3Endpoint optional S3 endpoint URL (e.g. http://minio:9000).
	// +optional
	S3Endpoint string `json:"s3Endpoint,omitempty"`

	// S3Bucket optional S3 bucket name.
	// +optional
	S3Bucket string `json:"s3Bucket,omitempty"`

	// S3AccessKey optional S3 access key.
	// +optional
	S3AccessKey string `json:"s3AccessKey,omitempty"`

	// S3SecretKey optional S3 secret key.
	// +optional
	S3SecretKey string `json:"s3SecretKey,omitempty"`

	// S3Region optional S3 region (default: us-east-1).
	// +optional
	S3Region string `json:"s3Region,omitempty"`
}

// MatrixDBBackupSpec defines the desired state of MatrixDBBackup.
type MatrixDBBackupSpec struct {
	// ClusterName specifies the MatrixDBCluster to back up.
	ClusterName string `json:"clusterName"`

	// Storage specifies the storage location for backup archives.
	// +optional
	Storage BackupStorageConfig `json:"storage,omitempty"`

	// BackupType specifies whether the backup is Logical (mysqldump) or Physical (xtrabackup).
	// Defaults to Logical.
	// +kubebuilder:default="Logical"
	// +optional
	BackupType string `json:"backupType,omitempty"`
}

// MatrixDBBackupStatus defines the observed state of MatrixDBBackup.
type MatrixDBBackupStatus struct {
	// Phase is the current status of the backup.
	Phase BackupPhase `json:"phase,omitempty"`

	// BackupPath is the full file path of the completed backup artifact.
	// +optional
	BackupPath string `json:"backupPath,omitempty"`

	// BackupSize is the human-readable size of the completed backup file.
	// +optional
	BackupSize string `json:"backupSize,omitempty"`

	// ExecutedGTID is the GTID coordinate at which the snapshot was taken.
	// +optional
	ExecutedGTID string `json:"executedGtid,omitempty"`

	// BinlogFile is the binary log filename at the time of backup.
	// +optional
	BinlogFile string `json:"binlogFile,omitempty"`

	// BinlogPos is the binary log position at the time of backup.
	// +optional
	BinlogPos uint64 `json:"binlogPos,omitempty"`

	// SourcePod is the Pod name from which the backup was dumped.
	// +optional
	SourcePod string `json:"sourcePod,omitempty"`

	// StartTime is when the backup process began.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the backup process succeeded or failed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message contains any diagnostic or failure messages.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mdbbk
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.clusterName"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Size",type="string",JSONPath=".status.backupSize"
// +kubebuilder:printcolumn:name="GTID",type="string",JSONPath=".status.executedGtid"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MatrixDBBackup is the Schema for the matrixdbbackups API.
type MatrixDBBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MatrixDBBackupSpec   `json:"spec,omitempty"`
	Status MatrixDBBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MatrixDBBackupList contains a list of MatrixDBBackup.
type MatrixDBBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MatrixDBBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MatrixDBBackup{}, &MatrixDBBackupList{})
}
