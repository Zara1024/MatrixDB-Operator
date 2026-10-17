# MatrixDB Operator Architecture & Engineering Design

This document describes the high-level architecture, reconciliation loops, and engineering principles behind **MatrixDB Operator v3.0.0**.

---

## 1. High-Level Architecture

`
                               +--------------------------------------------------+
                               |                 Client Workloads                 |
                               +--------------------------------------------------+
                                        | (Write)                      | (Read)
                                        v                              v
                       +---------------------------------+  +---------------------------------+
                       |    <cluster>-primary Service    |  |    <cluster>-replica Service    |
                       |      (role=primary selector)    |  |     (role=replica selector)     |
                       +---------------------------------+  +---------------------------------+
                                        |                                       |
                   +--------------------+                 +---------------------+
                   |                                      |
                   v                                      v
       +-----------------------+              +-----------------------+
       |     Pod 0 (Primary)   |              |     Pod 1 (Replica)   |
       | +-------------------+ | Semi-Sync    | +-------------------+ |
       | | mysqld (8.0/8.4)  | |<============>| | mysqld (8.0/8.4)  | |
       | | super_read_only=0 | | Replication  | | super_read_only=1 | |
       | +-------------------+ |              | +-------------------+ |
       | | PVC (Storage)     | |              | | PVC (Storage)     | |
       | +-------------------+ |              | +-------------------+ |
       +-----------------------+              +-----------------------+
                   ^                                      ^
                   |                                      |
                   +------------------+-------------------+
                                      |
                                      | Native Wire Protocol (database/sql connection pool)
                                      v
                      +-------------------------------+
                      |   MatrixDB Cluster Controller |
                      |   - Topology Reconciler       |
                      |   - Zero-Loss Election Engine |
                      |   - Continuous S3 Archiver    |
                      |   - DBA Doctor Lock Killer    |
                      |   - PDB & Quorum Guard        |
                      +-------------------------------+
`

---

## 2. Core Subsystems

### 2.1 Lossless Semi-Synchronous Replication (RPO = 0)
- Primary node commits transactions only after at least (replicas - 1) / 2 replicas have written the transaction to their local relay logs (AFTER_SYNC).
- To prevent deadlocks when replicas temporarily go offline, pl_semi_sync_master_wait_no_slave = 0 is configured dynamically by the controller.
- All non-primary nodes enforce super_read_only = ON to physically reject out-of-band writes.

### 2.2 Sub-Second Automatic Failover
1. **Heartbeat Failure Detection**: When the primary fails health checks (ping, connection timeout), the cluster enters PhaseFailover.
2. **Candidate Selection**: The controller queries all alive replicas for Executed_Gtid_Set. Candidates with the largest GTID sets are prioritized.
3. **Errant GTID Check**: Any node with errant transactions (GTID_SUBTRACT(replica, primary) != ") is disqualified from becoming primary.
4. **Promotion**:
 - Chosen replica executes STOP REPLICA; RESET REPLICA ALL; SET GLOBAL super_read_only = OFF;.
 - Kubernetes Service labels (pps.matrixdb.io/role: primary) are atomic switched to redirect ingress write traffic immediately.
5. **Topology Re-anchoring**: Remaining replicas are pointed to the new primary using CHANGE REPLICATION SOURCE TO ... SOURCE_AUTO_POSITION = 1;.

### 2.3 Continuous S3 Binlog Archiver & Point-in-Time Recovery (PITR)
- **Archiving Loop**: Runs in the background, continuously streaming binlog snapshot deltas to S3/MinIO under <cluster>/binlogs/.
- **Restoration Sequence**:
 1. Base logical/physical snapshot is downloaded and restored.
 2. All archived binlogs up to the target timestamp are downloaded sequentially.
 3. The controller replays transactions up to the exact requested point in time.

### 2.4 Autonomous DBA Doctor (MDL Lock Killer)
- Detects long-running read transactions or queries holding Metadata Locks (MDL) that block online DDL statements.
- Automatically inspects performance_schema.metadata_locks and terminates (KILL <connection_id>) the blocker if blocked time exceeds killLongMDLSeconds.

### 2.5 Multi-AZ TopologySpreadConstraints
- StatefulSet pods are automatically configured with opologySpreadConstraints across kubernetes.io/hostname and topology zones.
- Guarantees high availability by distributing database nodes across separate fault domains.

### 2.6 Kubernetes Validating Webhook
- Operates on port 9443 with TLS certificates automatically generated at runtime.
- Intercepts CRD create/update operations at the Kubernetes API Server layer and returns HTTP 403 Forbidden on illegal configurations (e.g. eplicas < 1).
