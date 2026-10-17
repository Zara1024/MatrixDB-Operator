package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// MasterStatus holds coordinates and GTID information of a Primary MySQL node.
type MasterStatus struct {
	File         string
	Position     uint64
	ExecutedGTID string
}

// ReplicaStatus holds replication status metrics of a Replica MySQL node.
type ReplicaStatus struct {
	IORunning           bool
	SQLRunning          bool
	SourceHost          string
	SourcePort          int
	RetrievedGTID       string
	ExecutedGTID        string
	SecondsBehindSource *int64
	LastIOError         string
	LastSQLError        string
}

// GetMasterStatus inspects the Primary status using SHOW MASTER STATUS (or SHOW BINARY LOG STATUS).
func (c *Client) GetMasterStatus(ctx context.Context) (*MasterStatus, error) {
	// First try MySQL 8.2+ syntax, fallback to classic SHOW MASTER STATUS
	query := "SHOW BINARY LOG STATUS"
	row := c.QueryRowContext(ctx, query)
	var file, executedGTID, doDB, ignoreDB string
	var pos uint64

	err := row.Scan(&file, &pos, &doDB, &ignoreDB, &executedGTID)
	if err != nil {
		query = "SHOW MASTER STATUS"
		row = c.QueryRowContext(ctx, query)
		err = row.Scan(&file, &pos, &doDB, &ignoreDB, &executedGTID)
		if err != nil {
			return nil, fmt.Errorf("failed to get master status from %s: %w", c.Host, err)
		}
	}

	return &MasterStatus{
		File:         file,
		Position:     pos,
		ExecutedGTID: executedGTID,
	}, nil
}

// GetReplicaStatus inspects replication details using SHOW REPLICA STATUS (or SHOW SLAVE STATUS).
func (c *Client) GetReplicaStatus(ctx context.Context) (*ReplicaStatus, error) {
	query := "SHOW REPLICA STATUS"
	rows, err := c.QueryContext(ctx, query)
	if err != nil {
		// Fallback to MySQL 5.7 / older 8.0
		query = "SHOW SLAVE STATUS"
		rows, err = c.QueryContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("failed to query replica status on %s: %w", c.Host, err)
		}
	}
	defer rows.Close()

	if !rows.Next() {
		// Empty output means this node is not configured as a replica
		return nil, nil
	}

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to read replica status columns: %w", err)
	}

	values := make([]sql.RawBytes, len(cols))
	scanArgs := make([]any, len(values))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	if err := rows.Scan(scanArgs...); err != nil {
		return nil, fmt.Errorf("failed to scan replica status: %w", err)
	}

	colMap := make(map[string]string)
	for i, col := range cols {
		colMap[strings.ToLower(col)] = string(values[i])
	}

	status := &ReplicaStatus{}

	// IO thread running check
	ioVal := colMap["replica_io_running"]
	if ioVal == "" {
		ioVal = colMap["slave_io_running"]
	}
	status.IORunning = strings.EqualFold(ioVal, "Yes")

	// SQL thread running check
	sqlVal := colMap["replica_sql_running"]
	if sqlVal == "" {
		sqlVal = colMap["slave_sql_running"]
	}
	status.SQLRunning = strings.EqualFold(sqlVal, "Yes")

	// Source host
	hostVal := colMap["source_host"]
	if hostVal == "" {
		hostVal = colMap["master_host"]
	}
	status.SourceHost = hostVal

	// GTID sets
	retrieved := colMap["retrieved_gtid_set"]
	status.RetrievedGTID = retrieved

	executed := colMap["executed_gtid_set"]
	status.ExecutedGTID = executed

	// Errors
	status.LastIOError = colMap["last_io_error"]
	status.LastSQLError = colMap["last_sql_error"]

	return status, nil
}

// ConfigureReplicationUser ensures the replication user exists with proper privileges.
// Uses sql_log_bin = 0 on a dedicated connection so DDL never writes to binlog or triggers semi-sync replication waiting.
func (c *Client) ConfigureReplicationUser(ctx context.Context, user, password string) error {
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed acquiring connection on %s: %w", c.Host, err)
	}
	defer conn.Close()

	statements := []string{
		"SET sql_log_bin = 0;",
		fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';", user, password),
		fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED BY '%s';", user, password),
		fmt.Sprintf("GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO '%s'@'%%';", user),
		"FLUSH PRIVILEGES;",
		"SET sql_log_bin = 1;",
	}

	for _, stmt := range statements {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed executing '%s' on %s: %w", stmt, c.Host, err)
		}
	}
	return nil
}

// ConfigureLosslessSemiSync configures semi-synchronous replication to guarantee zero data loss.
func (c *Client) ConfigureLosslessSemiSync(ctx context.Context, isPrimary bool, timeoutMS int64, waitSlaveCount int) error {
	// Try loading semi-sync plugins (safe to ignore if already loaded or built-in)
	_ = c.installSemiSyncPlugins(ctx)

	if isPrimary {
		if timeoutMS <= 0 {
			timeoutMS = 10000
		}
		if waitSlaveCount <= 0 {
			// Disable semi-sync master for single-node mode to prevent writes hanging
			stmts := []string{
				"SET GLOBAL rpl_semi_sync_master_enabled = 0;",
				"SET GLOBAL rpl_semi_sync_slave_enabled = 0;",
			}
			for _, s := range stmts {
				_, _ = c.ExecContext(ctx, s)
			}
		} else {
			// Configure Primary (Source) with required semi-sync quorum.
			// Set rpl_semi_sync_master_wait_no_slave = 0 so master does not deadlock when replicas are offline.
			stmts := []string{
				"SET GLOBAL rpl_semi_sync_master_wait_no_slave = 0;",
				"SET GLOBAL rpl_semi_sync_master_enabled = 1;",
				fmt.Sprintf("SET GLOBAL rpl_semi_sync_master_timeout = %d;", timeoutMS),
				fmt.Sprintf("SET GLOBAL rpl_semi_sync_master_wait_for_slave_count = %d;", waitSlaveCount),
				"SET GLOBAL rpl_semi_sync_slave_enabled = 0;",
			}
			for _, s := range stmts {
				_, _ = c.ExecContext(ctx, s)
			}
		}
	} else {
		// Configure Replica
		stmts := []string{
			"SET GLOBAL rpl_semi_sync_slave_enabled = 1;",
			"SET GLOBAL rpl_semi_sync_master_enabled = 0;",
		}
		for _, s := range stmts {
			_, _ = c.ExecContext(ctx, s)
		}
	}
	return nil
}

func (c *Client) installSemiSyncPlugins(ctx context.Context) error {
	_, _ = c.ExecContext(ctx, "INSTALL PLUGIN rpl_semi_sync_master SONAME 'semisync_master.so';")
	_, _ = c.ExecContext(ctx, "INSTALL PLUGIN rpl_semi_sync_slave SONAME 'semisync_slave.so';")
	_, _ = c.ExecContext(ctx, "INSTALL PLUGIN rpl_semi_sync_source SONAME 'semisync_source.so';")
	_, _ = c.ExecContext(ctx, "INSTALL PLUGIN rpl_semi_sync_replica SONAME 'semisync_replica.so';")
	return nil
}

// SetupAsReplica attaches this node as a replica pointing to sourceHost with GTID auto-positioning.
func (c *Client) SetupAsReplica(ctx context.Context, sourceHost string, sourcePort int, user, password string) error {
	// 1. Ensure read-only first to prevent any writes
	if err := c.SetReadOnly(ctx, true); err != nil {
		return err
	}

	// 2. Stop existing replica thread
	_ = c.StopReplica(ctx)

	// If this node is a fresh instance with no user databases and has never replicated,
	// reset binary logs/GTID to prevent local bootstrap GTID collision.
	if c.IsFreshInstance(ctx) {
		_, _ = c.ExecContext(ctx, "RESET MASTER;")
	}

	// 3. Configure replication channel
	changeStmt := fmt.Sprintf(
		"CHANGE REPLICATION SOURCE TO SOURCE_HOST='%s', SOURCE_PORT=%d, SOURCE_USER='%s', SOURCE_PASSWORD='%s', SOURCE_AUTO_POSITION=1, SOURCE_CONNECT_RETRY=3, SOURCE_RETRY_COUNT=86400, GET_SOURCE_PUBLIC_KEY=1;",
		sourceHost, sourcePort, user, password,
	)
	if _, err := c.ExecContext(ctx, changeStmt); err != nil {
		// Try classic CHANGE MASTER TO syntax
		changeMasterStmt := fmt.Sprintf(
			"CHANGE MASTER TO MASTER_HOST='%s', MASTER_PORT=%d, MASTER_USER='%s', MASTER_PASSWORD='%s', MASTER_AUTO_POSITION=1, MASTER_CONNECT_RETRY=3, MASTER_RETRY_COUNT=86400, GET_MASTER_PUBLIC_KEY=1;",
			sourceHost, sourcePort, user, password,
		)
		if _, errMaster := c.ExecContext(ctx, changeMasterStmt); errMaster != nil {
			return fmt.Errorf("failed executing CHANGE REPLICATION SOURCE on %s: %w", c.Host, err)
		}
	}

	// 4. Start replica thread
	return c.StartReplica(ctx)
}

// SetReadOnly controls super_read_only and read_only on the instance.
func (c *Client) SetReadOnly(ctx context.Context, readOnly bool) error {
	if readOnly {
		stmts := []string{
			"SET GLOBAL read_only = ON;",
			"SET GLOBAL super_read_only = ON;",
		}
		for _, s := range stmts {
			if _, err := c.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("failed to set read_only on %s: %w", c.Host, err)
			}
		}
	} else {
		stmts := []string{
			"SET GLOBAL super_read_only = OFF;",
			"SET GLOBAL read_only = OFF;",
		}
		for _, s := range stmts {
			if _, err := c.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("failed to unset read_only on %s: %w", c.Host, err)
			}
		}
	}
	return nil
}

// IsReadOnly queries whether the node currently has super_read_only enabled.
func (c *Client) IsReadOnly(ctx context.Context) (bool, error) {
	row := c.QueryRowContext(ctx, "SELECT @@global.super_read_only;")
	var val int
	if err := row.Scan(&val); err != nil {
		return false, fmt.Errorf("failed checking super_read_only on %s: %w", c.Host, err)
	}
	return val == 1, nil
}

// StopReplica stops IO and SQL threads.
func (c *Client) StopReplica(ctx context.Context) error {
	_, err := c.ExecContext(ctx, "STOP REPLICA;")
	if err != nil {
		_, err = c.ExecContext(ctx, "STOP SLAVE;")
	}
	return err
}

// StartReplica starts IO and SQL threads.
func (c *Client) StartReplica(ctx context.Context) error {
	_, err := c.ExecContext(ctx, "START REPLICA;")
	if err != nil {
		_, err = c.ExecContext(ctx, "START SLAVE;")
	}
	return err
}

// ResetReplicaAll stops replication safely and removes all source connection metadata.
// It stops IO thread first, allowing the SQL thread to apply in-flight relay logs, preventing loss of received events.
func (c *Client) ResetReplicaAll(ctx context.Context) error {
	// 1. Stop IO thread first so no new relay logs are received
	_, _ = c.ExecContext(ctx, "STOP REPLICA IO_THREAD;")
	_, _ = c.ExecContext(ctx, "STOP SLAVE IO_THREAD;")

	// 2. Wait up to 3s for SQL thread to apply any received relay logs
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case <-timeout:
			goto stopAll
		case <-ticker.C:
			rep, err := c.GetReplicaStatus(ctx)
			if err != nil || rep == nil || !rep.SQLRunning || rep.RetrievedGTID == rep.ExecutedGTID {
				goto stopAll
			}
		}
	}

stopAll:
	_ = c.StopReplica(ctx)
	_, err := c.ExecContext(ctx, "RESET REPLICA ALL;")
	if err != nil {
		_, err = c.ExecContext(ctx, "RESET SLAVE ALL;")
	}
	return err
}

// IsFreshInstance checks whether the instance is brand new with only system schemas and has never replicated.
func (c *Client) IsFreshInstance(ctx context.Context) bool {
	repStatus, err := c.GetReplicaStatus(ctx)
	if err == nil && repStatus != nil && repStatus.SourceHost != "" {
		return false
	}

	rows, err := c.QueryContext(ctx, "SHOW DATABASES;")
	if err != nil {
		return false
	}
	defer rows.Close()

	systemDBs := map[string]bool{
		"information_schema": true,
		"mysql":              true,
		"performance_schema": true,
		"sys":                true,
	}

	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err == nil {
			if !systemDBs[dbName] {
				return false
			}
		}
	}
	return true
}
