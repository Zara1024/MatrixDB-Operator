package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MatrixDBScheduledBackupSpec defines the desired state of scheduled backups.
type MatrixDBScheduledBackupSpec struct {
	// ClusterName specifies the MatrixDBCluster to periodically back up.
	ClusterName string `json:"clusterName"`

	// Schedule is a standard cron expression (e.g. "0 2 * * *" or "*/5 * * * *").
	Schedule string `json:"schedule"`

	// RetentionDays specifies how many days backups should be retained before cleanup.
	// +optional
	RetentionDays int32 `json:"retentionDays,omitempty"`

	// Storage defines storage destination (Local / S3).
	// +optional
	Storage BackupStorageConfig `json:"storage,omitempty"`
}

// MatrixDBScheduledBackupStatus defines the observed state of scheduled backups.
type MatrixDBScheduledBackupStatus struct {
	// LastBackupTime is when the last backup was successfully triggered.
	// +optional
	LastBackupTime *metav1.Time `json:"lastBackupTime,omitempty"`

	// LastBackupName is the name of the last generated MatrixDBBackup resource.
	// +optional
	LastBackupName string `json:"lastBackupName,omitempty"`

	// NextScheduleTime indicates the next expected execution.
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`

	// BackupsCount is the total number of backups managed by this schedule.
	// +optional
	BackupsCount int `json:"backupsCount,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mdbschbk
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.clusterName"
// +kubebuilder:printcolumn:name="Schedule",type="string",JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="LastBackup",type="string",JSONPath=".status.lastBackupName"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MatrixDBScheduledBackup is the Schema for the matrixdbscheduledbackups API.
type MatrixDBScheduledBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MatrixDBScheduledBackupSpec   `json:"spec,omitempty"`
	Status MatrixDBScheduledBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MatrixDBScheduledBackupList contains a list of MatrixDBScheduledBackup.
type MatrixDBScheduledBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MatrixDBScheduledBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MatrixDBScheduledBackup{}, &MatrixDBScheduledBackupList{})
}
