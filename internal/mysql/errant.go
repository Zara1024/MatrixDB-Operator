package mysql

import (
	"context"
	"fmt"
	"strings"
)

// GetErrantGTID calculates any errant transactions on this instance relative to the primary GTID set.
// It executes SELECT GTID_SUBTRACT(@@GLOBAL.gtid_executed, ?) on the instance.
func (c *Client) GetErrantGTID(ctx context.Context, primaryExecutedGTID string) (string, error) {
	if strings.TrimSpace(primaryExecutedGTID) == "" {
		return "", nil
	}
	query := "SELECT GTID_SUBTRACT(@@GLOBAL.gtid_executed, ?);"
	var diff string
	err := c.QueryRowContext(ctx, query, primaryExecutedGTID).Scan(&diff)
	if err != nil {
		return "", fmt.Errorf("failed executing GTID_SUBTRACT on %s: %w", c.Host, err)
	}
	return strings.TrimSpace(diff), nil
}
