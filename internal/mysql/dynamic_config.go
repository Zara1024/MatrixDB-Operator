package mysql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// KnownDynamicVariables maps lowercase MySQL system variable names that can be safely
// persisted and reloaded at runtime using SET PERSIST without requiring Pod restarts.
var KnownDynamicVariables = map[string]bool{
	"max_connections":                true,
	"innodb_buffer_pool_size":        true,
	"slow_query_log":                 true,
	"long_query_time":                true,
	"max_connect_errors":             true,
	"wait_timeout":                   true,
	"interactive_timeout":            true,
	"connect_timeout":                true,
	"sql_mode":                       true,
	"general_log":                    true,
	"sync_binlog":                    true,
	"innodb_flush_log_at_trx_commit": true,
	"innodb_max_dirty_pages_pct":     true,
	"binlog_expire_logs_seconds":     true,
	"table_open_cache":               true,
	"thread_cache_size":              true,
	"max_allowed_packet":             true,
}

// IsDynamicVariable checks whether a MySQL variable is dynamically configurable.
func IsDynamicVariable(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return KnownDynamicVariables[normalized]
}

// ParseSizeToBytes parses human size strings like "512M", "1G", "128K" to bytes integer.
func ParseSizeToBytes(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if len(s) == 0 {
		return 0, false
	}
	multiplier := int64(1)
	unit := s[len(s)-1]
	numStr := s
	if unit == 'K' {
		multiplier = 1024
		numStr = s[:len(s)-1]
	} else if unit == 'M' {
		multiplier = 1024 * 1024
		numStr = s[:len(s)-1]
	} else if unit == 'G' {
		multiplier = 1024 * 1024 * 1024
		numStr = s[:len(s)-1]
	} else if unit < '0' || unit > '9' {
		return 0, false
	}
	num, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, false
	}
	return num * multiplier, true
}

// AreValuesEquivalent checks if current MySQL value and target value are functionally identical.
func AreValuesEquivalent(current, target string) bool {
	c := strings.TrimSpace(strings.ToUpper(current))
	t := strings.TrimSpace(strings.ToUpper(target))
	if c == t {
		return true
	}
	// Boolean equivalents: "1" == "ON" / "TRUE", "0" == "OFF" / "FALSE"
	if (c == "1" && (t == "ON" || t == "TRUE")) || (c == "0" && (t == "OFF" || t == "FALSE")) {
		return true
	}
	if ((c == "ON" || c == "TRUE") && t == "1") || ((c == "OFF" || c == "FALSE") && t == "0") {
		return true
	}
	// Byte sizes comparison
	cBytes, cOk := ParseSizeToBytes(c)
	tBytes, tOk := ParseSizeToBytes(t)
	if cOk && tOk {
		return cBytes == tBytes
	}
	return false
}

// GetGlobalVariable retrieves the current runtime global value of a MySQL variable.
func (c *Client) GetGlobalVariable(ctx context.Context, name string) (string, error) {
	row := c.QueryRowContext(ctx, fmt.Sprintf("SELECT @@GLOBAL.%s;", name))
	var val string
	if err := row.Scan(&val); err != nil {
		return "", err
	}
	return val, nil
}

// SetPersistVariable sets and persists a dynamic variable on the target MySQL instance.
func (c *Client) SetPersistVariable(ctx context.Context, name, value string) error {
	formattedVal := formatMySQLValue(value)
	query := fmt.Sprintf("SET PERSIST %s = %s;", name, formattedVal)
	_, err := c.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed executing '%s': %w", query, err)
	}
	return nil
}

func formatMySQLValue(val string) string {
	trimmed := strings.TrimSpace(val)
	upper := strings.ToUpper(trimmed)
	if upper == "ON" || upper == "OFF" || upper == "TRUE" || upper == "FALSE" {
		return upper
	}
	if bytes, ok := ParseSizeToBytes(trimmed); ok {
		return fmt.Sprintf("%d", bytes)
	}
	if _, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return trimmed
	}
	if _, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return trimmed
	}
	// Escape single quotes for SQL string literal
	escaped := strings.ReplaceAll(trimmed, "'", "''")
	return fmt.Sprintf("'%s'", escaped)
}
