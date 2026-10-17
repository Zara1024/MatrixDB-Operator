package constants

const (
	LabelAppName      = "app.kubernetes.io/name"
	LabelAppInstance = "app.kubernetes.io/instance"
	LabelAppManagedBy = "app.kubernetes.io/managed-by"
	LabelClusterName  = "apps.matrixdb.io/cluster"
	LabelNodeRole     = "apps.matrixdb.io/role"

	RolePrimary = "primary"
	RoleReplica = "replica"

	MySQLPort = 3306

	SecretRootPasswordKey    = "root-password"
	SecretReplicaPasswordKey = "replica-password"
	ReplicationUser          = "matrixdb_replica"

	ConfigMapVolumeName  = "mysql-config"
	ServerIDVolumeName   = "server-id-config"
	DataVolumeName       = "data"

	DefaultServerIDOffset = 100
	DefaultSemiSyncTimeoutMS = 1000000000
)
