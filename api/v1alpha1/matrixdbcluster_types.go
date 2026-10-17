package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StorageConfig defines storage settings for MySQL PVCs.
type StorageConfig struct {
	// StorageClassName is the storage class to use. If empty, the default storage class is used.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// Size is the requested persistent volume size (e.g. 10Gi, 50Gi).
	Size string `json:"size"`
}

// ClusterPhase represents the high-level health phase of the MySQL cluster.
type ClusterPhase string

const (
	PhasePending      ClusterPhase = "Pending"
	PhaseInitializing ClusterPhase = "Initializing"
	PhaseHealthy      ClusterPhase = "Healthy"
	PhaseFailover     ClusterPhase = "Failover"
	PhaseDegraded     ClusterPhase = "Degraded"
)

// NodeRole represents whether a MySQL node is Primary or Replica.
type NodeRole string

const (
	NodeRolePrimary NodeRole = "Primary"
	NodeRoleReplica NodeRole = "Replica"
	NodeRoleUnknown NodeRole = "Unknown"
)

// NodeStatus defines the live status of an individual MySQL node.
type NodeStatus struct {
	// PodName is the name of the StatefulSet Pod.
	PodName string `json:"podName"`

	// Role indicates whether the node is Primary or Replica.
	Role NodeRole `json:"role"`

	// Ready indicates whether the Pod has passed all readiness probes.
	Ready bool `json:"ready"`

	// ReadOnly indicates whether super_read_only is active on this MySQL instance.
	ReadOnly bool `json:"readOnly"`

	// ExecutedGTID is the Executed_Gtid_Set retrieved from the instance.
	// +optional
	ExecutedGTID string `json:"executedGtid,omitempty"`

	// RetrievedGTID is the Retrieved_Gtid_Set retrieved from the replica.
	// +optional
	RetrievedGTID string `json:"retrievedGtid,omitempty"`

	// IORunning indicates if Slave_IO_Running / Replica_IO_Running is Yes.
	// +optional
	IORunning bool `json:"ioRunning,omitempty"`

	// SQLRunning indicates if Slave_SQL_Running / Replica_SQL_Running is Yes.
	// +optional
	SQLRunning bool `json:"sqlRunning,omitempty"`

	// Errant indicates if the node contains errant GTID transactions not present on Primary.
	// +optional
	Errant bool `json:"errant,omitempty"`

	// ErrantGTID contains the errant GTID transaction set, if any.
	// +optional
	ErrantGTID string `json:"errantGtid,omitempty"`

	// Message contains any warnings or diagnostic details.
	// +optional
	Message string `json:"message,omitempty"`
}

// MetricsConfig defines Prometheus exporter configuration.
type MetricsConfig struct {
	// Enabled toggles whether the mysqld_exporter sidecar is injected.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Image is the mysqld_exporter image (default: prom/mysqld-exporter:v0.15.1).
	// +optional
	Image string `json:"image,omitempty"`

	// Port is the port number for the metrics exporter (default: 9104).
	// +optional
	Port int32 `json:"port,omitempty"`

	// Resources describes compute resource requirements for the exporter container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// ProxyConfig defines ProxySQL connection pool and query routing settings.
type ProxyConfig struct {
	// Enabled toggles whether a ProxySQL layer is deployed for the cluster.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Image is the ProxySQL container image (default: proxysql/proxysql:2.5.5).
	// +optional
	Image string `json:"image,omitempty"`

	// Replicas is the number of ProxySQL instances for HA (default: 2).
	// +kubebuilder:default=2
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
}

// DBADoctorConfig defines automated diagnostic and self-healing DBA maintenance.
type DBADoctorConfig struct {
	// Enabled toggles whether the DBA Doctor is active (default true).
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// KillLongMDLSeconds kills transactions waiting on or blocking metadata locks for longer than this duration (default 10s).
	// +kubebuilder:default=10
	// +optional
	KillLongMDLSeconds int32 `json:"killLongMDLSeconds,omitempty"`

	// MaxReplicationLagSeconds automatically adjusts parallel replication workers when lag exceeds this threshold (default 10s).
	// +kubebuilder:default=10
	// +optional
	MaxReplicationLagSeconds int32 `json:"maxReplicationLagSeconds,omitempty"`
}

// MatrixDBClusterSpec defines the desired state of MatrixDBCluster.
type MatrixDBClusterSpec struct {
	// Replicas is the desired number of MySQL instances (minimum 1, recommended 3 for HA).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	Replicas int32 `json:"replicas"`

	// AnalyticalReplicas is the number of dedicated read-only analytical replicas isolated from OLTP traffic.
	// +optional
	AnalyticalReplicas int32 `json:"analyticalReplicas,omitempty"`

	// Image is the MySQL Docker image (e.g. mysql:8.0.36 or mysql:8.4.0).
	// +kubebuilder:default="mysql:8.0.36"
	Image string `json:"image"`

	// Storage defines persistent storage requirements for the data directory.
	Storage StorageConfig `json:"storage"`

	// Resources describes compute resource requirements for the mysqld container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// MysqlConfig contains custom my.cnf configuration directives to overlay.
	// +optional
	MysqlConfig map[string]string `json:"mysqlConfig,omitempty"`

	// SecretName points to a Secret containing `root-password` and `replica-password`.
	// If omitted, MatrixDB Operator automatically generates and manages a secure Secret.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// Metrics defines Prometheus metrics exporter settings.
	// +optional
	Metrics *MetricsConfig `json:"metrics,omitempty"`

	// Proxy defines ProxySQL query routing and connection pool settings.
	// +optional
	Proxy *ProxyConfig `json:"proxy,omitempty"`

	// Doctor defines autonomous DBA self-healing capabilities (MDL killer, lag optimizer).
	// +optional
	Doctor *DBADoctorConfig `json:"doctor,omitempty"`

	// CloneFromCluster names an existing MatrixDBCluster to clone from upon initialization.
	// +optional
	CloneFromCluster string `json:"cloneFromCluster,omitempty"`

	// IsReplicaCluster specifies if this cluster is a standby cluster replicating from an external primary.
	// +optional
	IsReplicaCluster bool `json:"isReplicaCluster,omitempty"`

	// PrimaryCluster specifies the upstream primary cluster endpoint for cross-region disaster recovery.
	// +optional
	PrimaryCluster string `json:"primaryCluster,omitempty"`

	// ServerIDOffset is an offset added to the Pod ordinal to generate server-id (default 100).
	// +kubebuilder:default=100
	// +optional
	ServerIDOffset int `json:"serverIdOffset,omitempty"`

	// SemiSyncTimeoutMS is the semi-synchronous replication timeout in milliseconds.
	// Defaults to 1000000000 (practically infinite, ensuring zero loss without falling back to async).
	// +kubebuilder:default=1000000000
	// +optional
	SemiSyncTimeoutMS int64 `json:"semiSyncTimeoutMs,omitempty"`

	// Affinity allows configuring node or pod affinity for the StatefulSet.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Tolerations allows configuring pod tolerations.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// NodeSelector is a selector which must be true for the pod to fit on a node.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// BinlogArchive defines settings for real-time continuous binlog streaming to S3 for PITR.
	// +optional
	BinlogArchive *BinlogArchiveConfig `json:"binlogArchive,omitempty"`

	// TopologySpreadConstraints specifies topology spread rules for distributing MySQL Pods across AZs.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}

// BinlogArchiveConfig defines settings for continuous binlog archiving to S3 for Point-in-Time Recovery.
type BinlogArchiveConfig struct {
	// Enabled toggles whether binlog archiving is active.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// S3Storage specifies S3 destination for continuous binlog archive.
	// +optional
	S3Storage BackupStorageConfig `json:"s3Storage,omitempty"`

	// ArchiveIntervalSeconds specifies the interval for archiving rotated binlogs (default 10s).
	// +kubebuilder:default=10
	// +optional
	ArchiveIntervalSeconds int `json:"archiveIntervalSeconds,omitempty"`
}

// MatrixDBClusterStatus defines the observed state of MatrixDBCluster.
type MatrixDBClusterStatus struct {
	// Phase is the current operational phase of the cluster.
	Phase ClusterPhase `json:"phase,omitempty"`

	// Primary is the Pod name of the current primary (master) MySQL node.
	Primary string `json:"primary,omitempty"`

	// ReadyReplicas is the number of Pods currently in Ready state.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// SyncedReplicas is the number of replicas actively synchronizing via semi-sync.
	SyncedReplicas int32 `json:"syncedReplicas,omitempty"`

	// ErrantReplicas is the count of replica nodes quarantined due to errant transactions.
	// +optional
	ErrantReplicas int32 `json:"errantReplicas,omitempty"`

	// AnalyticalService is the Kubernetes Service endpoint for analytical replicas.
	// +optional
	AnalyticalService string `json:"analyticalService,omitempty"`

	// Upgrading indicates whether a rolling upgrade is currently underway.
	// +optional
	Upgrading bool `json:"upgrading,omitempty"`

	// Conditions represents the latest available observations of the cluster's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Nodes contains detailed per-node replication and health diagnostics.
	// +optional
	Nodes []NodeStatus `json:"nodes,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mdb
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Primary",type="string",JSONPath=".status.primary"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Synced",type="integer",JSONPath=".status.syncedReplicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MatrixDBCluster is the Schema for the matrixdbclusters API.
type MatrixDBCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MatrixDBClusterSpec   `json:"spec,omitempty"`
	Status MatrixDBClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MatrixDBClusterList contains a list of MatrixDBCluster.
type MatrixDBClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MatrixDBCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MatrixDBCluster{}, &MatrixDBClusterList{})
}
