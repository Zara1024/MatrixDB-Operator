package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MigrationPhase represents the lifecycle state of a schema migration.
type MigrationPhase string

const (
	MigrationPhasePending   MigrationPhase = "Pending"
	MigrationPhaseRunning   MigrationPhase = "Running"
	MigrationPhaseCompleted MigrationPhase = "Completed"
	MigrationPhaseFailed    MigrationPhase = "Failed"
)

// MatrixDBSchemaMigrationSpec defines the desired DDL schema migration.
type MatrixDBSchemaMigrationSpec struct {
	// ClusterName specifies the MatrixDBCluster where DDL will be executed.
	ClusterName string `json:"clusterName"`

	// Database specifies the target database name.
	Database string `json:"database"`

	// SQL contains the DDL or migration statements to execute.
	SQL string `json:"sql"`
}

// MatrixDBSchemaMigrationStatus defines the observed state of the schema migration.
type MatrixDBSchemaMigrationStatus struct {
	// Phase is the current phase of the migration.
	Phase MigrationPhase `json:"phase,omitempty"`

	// ExecutedAt is when the migration was successfully executed.
	// +optional
	ExecutedAt *metav1.Time `json:"executedAt,omitempty"`

	// ExecutionDuration is the human-readable duration of the DDL execution.
	// +optional
	ExecutionDuration string `json:"executionDuration,omitempty"`

	// ExecutedBy indicates the Primary Pod that executed the DDL.
	// +optional
	ExecutedBy string `json:"executedBy,omitempty"`

	// Message contains execution logs or error messages.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mdbmig
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.clusterName"
// +kubebuilder:printcolumn:name="Database",type="string",JSONPath=".spec.database"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MatrixDBSchemaMigration is the Schema for declarative DDL schema migrations.
type MatrixDBSchemaMigration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MatrixDBSchemaMigrationSpec   `json:"spec,omitempty"`
	Status MatrixDBSchemaMigrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MatrixDBSchemaMigrationList contains a list of MatrixDBSchemaMigration.
type MatrixDBSchemaMigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MatrixDBSchemaMigration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MatrixDBSchemaMigration{}, &MatrixDBSchemaMigrationList{})
}
