package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RestorePhase represents the lifecycle state of a MatrixDBRestore.
type RestorePhase string

const (
	RestorePhasePending   RestorePhase = "Pending"
	RestorePhaseRunning   RestorePhase = "Running"
	RestorePhaseCompleted RestorePhase = "Completed"
	RestorePhaseFailed    RestorePhase = "Failed"
)

// PITRConfig defines Point-in-Time-Recovery specifications.
type PITRConfig struct {
	// TargetTime specifies the target timestamp to recover to.
	// +optional
	TargetTime *metav1.Time `json:"targetTime,omitempty"`

	// TargetGTID specifies a target GTID coordinate to stop recovery at.
	// +optional
	TargetGTID string `json:"targetGtid,omitempty"`
}

// MatrixDBRestoreSpec defines the desired state of MatrixDBRestore.
type MatrixDBRestoreSpec struct {
	// BackupName specifies the name of the MatrixDBBackup resource to restore from.
	// +optional
	BackupName string `json:"backupName,omitempty"`

	// S3Storage specifies S3 storage location when restoring directly from an S3 object.
	// +optional
	S3Storage *BackupStorageConfig `json:"s3Storage,omitempty"`

	// S3Key specifies the object key/path inside the S3 bucket.
	// +optional
	S3Key string `json:"s3Key,omitempty"`

	// ClusterName specifies the target MatrixDBCluster to restore into.
	ClusterName string `json:"clusterName"`

	// PITR specifies point-in-time recovery settings.
	// +optional
	PITR *PITRConfig `json:"pitr,omitempty"`
}

// MatrixDBRestoreStatus defines the observed state of MatrixDBRestore.
type MatrixDBRestoreStatus struct {
	// Phase is the current phase of the restore operation.
	Phase RestorePhase `json:"phase,omitempty"`

	// SourceBackup is the name of the backup used for restore.
	// +optional
	SourceBackup string `json:"sourceBackup,omitempty"`

	// BackupPath is the file path or URI of the backup archive restored.
	// +optional
	BackupPath string `json:"backupPath,omitempty"`

	// RestoredGTID is the executed GTID coordinate after restoration.
	// +optional
	RestoredGTID string `json:"restoredGtid,omitempty"`

	// StartTime is when the restore began.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the restore process finished.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message contains human-readable diagnostic messages.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mdbrs
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.clusterName"
// +kubebuilder:printcolumn:name="Backup",type="string",JSONPath=".spec.backupName"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MatrixDBRestore is the Schema for the matrixdbrestores API.
type MatrixDBRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MatrixDBRestoreSpec   `json:"spec,omitempty"`
	Status MatrixDBRestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MatrixDBRestoreList contains a list of MatrixDBRestore.
type MatrixDBRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MatrixDBRestore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MatrixDBRestore{}, &MatrixDBRestoreList{})
}
