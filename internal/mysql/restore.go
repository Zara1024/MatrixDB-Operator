package mysql

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

// RestoreDatabase executes the SQL statements contained in the backup archive file.
func (c *Client) RestoreDatabase(ctx context.Context, sourcePath string) error {
	file, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("failed opening restore source file %s: %w", sourcePath, err)
	}
	defer file.Close()

	// Disable foreign key checks during restoration
	if _, err := c.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0;"); err != nil {
		return fmt.Errorf("failed disabling foreign key checks: %w", err)
	}
	defer func() {
		_, _ = c.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 1;")
	}()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 1024*1024) // 1MB buffer for large rows
	scanner.Buffer(buf, 10*1024*1024)

	var stmtBuilder strings.Builder

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "--") || strings.HasPrefix(line, "/*") {
			continue
		}

		stmtBuilder.WriteString(line)
		stmtBuilder.WriteString(" ")

		if strings.HasSuffix(line, ";") {
			stmt := strings.TrimSpace(stmtBuilder.String())
			stmtBuilder.Reset()

			if stmt == "" || stmt == ";" {
				continue
			}

			// Execute statement
			if _, err := c.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("failed executing statement: %w (stmt: %.120s)", err, stmt)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading restore file: %w", err)
	}

	return nil
}
