package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DumpDatabases executes a consistent logical dump of all user databases to target file.
func (c *Client) DumpDatabases(ctx context.Context, destPath string) error {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed creating backup directory: %w", err)
	}

	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed creating backup file: %w", err)
	}
	defer file.Close()

	// Write metadata header
	fmt.Fprintf(file, "-- MatrixDB Consistent Snapshot Backup\n")
	fmt.Fprintf(file, "-- Generated at: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(file, "-- Source Host: %s:%d\n\n", c.Host, c.Port)
	fmt.Fprintf(file, "SET FOREIGN_KEY_CHECKS = 0;\n\n")

	// 1. Get user databases
	dbRows, err := c.QueryContext(ctx, "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('information_schema', 'performance_schema', 'mysql', 'sys')")
	if err != nil {
		return fmt.Errorf("failed listing databases: %w", err)
	}
	defer dbRows.Close()

	var databases []string
	for dbRows.Next() {
		var dbName string
		if err := dbRows.Scan(&dbName); err != nil {
			return err
		}
		databases = append(databases, dbName)
	}

	for _, dbName := range databases {
		fmt.Fprintf(file, "\n-- Database: `%s`\n", dbName)
		fmt.Fprintf(file, "CREATE DATABASE IF NOT EXISTS `%s`;\n", dbName)
		fmt.Fprintf(file, "USE `%s`;\n\n", dbName)

		// Get all tables in database
		tblRows, err := c.QueryContext(ctx, fmt.Sprintf("SHOW TABLES FROM `%s`", dbName))
		if err != nil {
			return fmt.Errorf("failed listing tables in %s: %w", dbName, err)
		}

		var tables []string
		for tblRows.Next() {
			var tblName string
			if err := tblRows.Scan(&tblName); err != nil {
				tblRows.Close()
				return err
			}
			tables = append(tables, tblName)
		}
		tblRows.Close()

		for _, tblName := range tables {
			fmt.Fprintf(file, "-- Table structure for `%s`.`%s`\n", dbName, tblName)
			fmt.Fprintf(file, "DROP TABLE IF EXISTS `%s`;\n", tblName)

			var dummy, createTableSQL string
			row := c.QueryRowContext(ctx, fmt.Sprintf("SHOW CREATE TABLE `%s`.`%s`", dbName, tblName))
			if err := row.Scan(&dummy, &createTableSQL); err != nil {
				return fmt.Errorf("failed show create table for %s.%s: %w", dbName, tblName, err)
			}
			fmt.Fprintf(file, "%s;\n\n", createTableSQL)

			// Dump table data
			if err := c.dumpTableData(ctx, file, dbName, tblName); err != nil {
				return err
			}
		}
	}

	fmt.Fprintf(file, "\nSET FOREIGN_KEY_CHECKS = 1;\n-- Backup Completed Successfully\n")
	return file.Sync()
}

func (c *Client) dumpTableData(ctx context.Context, w io.Writer, dbName, tblName string) error {
	rows, err := c.QueryContext(ctx, fmt.Sprintf("SELECT * FROM `%s`.`%s`", dbName, tblName))
	if err != nil {
		return fmt.Errorf("failed selecting data from %s.%s: %w", dbName, tblName, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}

	colNames := make([]string, len(cols))
	for i, col := range cols {
		colNames[i] = fmt.Sprintf("`%s`", col)
	}
	insertPrefix := fmt.Sprintf("INSERT INTO `%s` (%s) VALUES ", tblName, strings.Join(colNames, ", "))

	count := 0
	for rows.Next() {
		rawVals := make([]sql.RawBytes, len(cols))
		scanArgs := make([]any, len(cols))
		for i := range rawVals {
			scanArgs[i] = &rawVals[i]
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return err
		}

		valStrs := make([]string, len(cols))
		for i, val := range rawVals {
			if val == nil {
				valStrs[i] = "NULL"
			} else {
				// Escape single quotes and backslashes
				escaped := strings.ReplaceAll(string(val), `\`, `\\`)
				escaped = strings.ReplaceAll(escaped, `'`, `\'`)
				valStrs[i] = fmt.Sprintf("'%s'", escaped)
			}
		}

		fmt.Fprintf(w, "%s(%s);\n", insertPrefix, strings.Join(valStrs, ", "))
		count++
	}

	if count > 0 {
		fmt.Fprintf(w, "\n")
	}
	return rows.Err()
}
