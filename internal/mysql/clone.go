package mysql

import (
	"context"
	"fmt"
)

// InstallClonePlugin installs the official MySQL 8.0 clone plugin.
func (c *Client) InstallClonePlugin(ctx context.Context) error {
	_, err := c.ExecContext(ctx, "INSTALL PLUGIN clone SONAME 'mysql_clone.so';")
	return err
}

// CloneInstanceFrom executes the physical CLONE INSTANCE FROM statement.
func (c *Client) CloneInstanceFrom(ctx context.Context, donorHost string, donorPort int, donorUser, donorPass string) error {
	stmt := fmt.Sprintf("CLONE INSTANCE FROM '%s'@'%s':%d IDENTIFIED BY '%s';",
		donorUser, donorHost, donorPort, donorPass)
	_, err := c.ExecContext(ctx, stmt)
	return err
}
