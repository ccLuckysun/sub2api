package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsSamplesClientRequestIDMigration(t *testing.T) {
	content, err := FS.ReadFile("253_ops_samples_client_request_id.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE ops_request_transfer_samples ADD COLUMN IF NOT EXISTS client_request_id TEXT NULL")
	require.Contains(t, sql, "COMMENT ON COLUMN ops_request_transfer_samples.client_request_id")
	require.NotContains(t, sql, "NOT NULL")
	require.NotContains(t, sql, "DROP COLUMN")
	require.NotContains(t, sql, "ALTER COLUMN")
	require.NotContains(t, sql, "UPDATE ")
}
