package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func v2FieldLog(request string, body, latency *int64) *service.UsageLog {
	return &service.UsageLog{UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: request, Model: "fixture", RequestedModel: "fixture", RateMultiplier: 1, CreatedAt: time.Now().UTC(), BodyBytes: body, GatewayLatencyMs: latency}
}

func TestUsageLogV2InsertNullableInt64(t *testing.T) {
	zero, body, latency := int64(0), int64(1<<40), int64(1<<35)
	for _, tc := range []struct {
		name          string
		body, latency *int64
	}{
		{"absent", nil, nil}, {"zero", &zero, &zero}, {"int64 values", &body, &latency}, {"independent null", &body, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := v2FieldLog("v2-"+tc.name, tc.body, tc.latency)
			prepared := prepareUsageLogInsert(log)
			require.Len(t, prepared.args, 66)
			require.Equal(t, log.CreatedAt, prepared.args[63], "all original argument positions remain unchanged")
			for i, value := range []*int64{tc.body, tc.latency} {
				require.Equal(t, "bigint", usageLogInsertArgTypes[64+i])
				got, err := driver.DefaultParameterConverter.ConvertValue(prepared.args[64+i])
				require.NoError(t, err)
				if value == nil {
					require.Nil(t, got)
				} else {
					require.Equal(t, *value, got)
				}
			}
			db, mock := newSQLMock(t)
			args := make([]driver.Value, len(prepared.args))
			for i, arg := range prepared.args {
				var err error
				args[i], err = driver.DefaultParameterConverter.ConvertValue(arg)
				require.NoError(t, err)
			}
			mock.ExpectQuery("INSERT INTO usage_logs").WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(8), log.CreatedAt))
			inserted, err := (&usageLogRepository{sql: db}).createSingle(context.Background(), db, log)
			require.NoError(t, err)
			require.True(t, inserted)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// Opt-in isolated PG fixture only. The database-name guard prevents this test
// from creating fixture objects in a normal developer/application database.
func TestUsageLogV2PostgresRoundTrip(t *testing.T) {
	dsn := os.Getenv("USAGE_V2_TEST_DSN")
	if dsn == "" {
		t.Skip("set USAGE_V2_TEST_DSN to isolated w4a2a_fixture PG18")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	var database string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT current_database()").Scan(&database))
	require.Equal(t, "w4a2a_fixture", database)
	columns := strings.Split(usageLogSelectColumns, ", ")
	require.Len(t, columns, 67)
	var definitions []string
	for i, name := range columns[1:65] {
		definitions = append(definitions, name+" "+usageLogInsertArgTypes[i])
	}
	_, err = db.ExecContext(ctx, "CREATE TABLE usage_logs(id BIGSERIAL PRIMARY KEY,"+strings.Join(definitions, ",")+",UNIQUE(request_id,api_key_id))")
	require.NoError(t, err)
	// An actual pre-migration row must read as NULL after the additive migration.
	historical := prepareUsageLogInsert(v2FieldLog("historical", nil, nil))
	var placeholders []string
	for i := 1; i <= 64; i++ {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
	}
	var historicalID int64
	err = db.QueryRowContext(ctx, "INSERT INTO usage_logs("+strings.Join(columns[1:65], ",")+") VALUES("+strings.Join(placeholders, ",")+") RETURNING id", historical.args[:64]...).Scan(&historicalID)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("252_add_usage_logs_v2_fields.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var columnCount int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_logs' AND column_name IN ('body_bytes','gateway_latency_ms') AND data_type='bigint' AND is_nullable='YES' AND column_default IS NULL").Scan(&columnCount))
	require.Equal(t, 2, columnCount)
	repo := &usageLogRepository{sql: db}
	old, err := repo.GetByID(ctx, historicalID)
	require.NoError(t, err)
	require.Nil(t, old.BodyBytes)
	require.Nil(t, old.GatewayLatencyMs)
	// Ent reads and raw SQL reads must agree about nil and explicit zero.
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	zero, body, latency := int64(0), int64(1<<40), int64(1<<35)
	for _, mode := range []string{"single", "batch", "best_effort", "no_result"} {
		for _, tc := range []struct {
			name          string
			body, latency *int64
		}{
			{"absent", nil, nil}, {"zero", &zero, &zero}, {"int64", &body, &latency}, {"mixed", nil, &latency},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				log := v2FieldLog(mode+"-"+tc.name, tc.body, tc.latency)
				prepared := prepareUsageLogInsert(log)
				switch mode {
				case "single":
					inserted, err := repo.createSingle(ctx, db, log)
					require.NoError(t, err)
					require.True(t, inserted)
				case "batch":
					key := usageLogBatchKey(log.RequestID, log.APIKeyID)
					query, args := buildUsageLogBatchInsertQuery([]string{key}, map[string]usageLogInsertPrepared{key: prepared})
					var payload []byte
					require.NoError(t, db.QueryRowContext(ctx, query, args...).Scan(&payload))
					var rows []usageLogBatchRow
					require.NoError(t, json.Unmarshal(payload, &rows))
					require.Len(t, rows, 1)
					require.True(t, rows[0].Inserted)
				case "best_effort":
					query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
					_, err := db.ExecContext(ctx, query, args...)
					require.NoError(t, err)
				case "no_result":
					require.NoError(t, execUsageLogInsertNoResult(ctx, db, prepared))
				}
				var id int64
				require.NoError(t, db.QueryRowContext(ctx, "SELECT id FROM usage_logs WHERE request_id=$1", log.RequestID).Scan(&id))
				got, err := repo.GetByID(ctx, id)
				require.NoError(t, err)
				require.Equal(t, tc.body, got.BodyBytes)
				require.Equal(t, tc.latency, got.GatewayLatencyMs)
				entity, err := client.UsageLog.Get(ctx, id)
				require.NoError(t, err)
				require.Equal(t, tc.body, entity.BodyBytes)
				require.Equal(t, tc.latency, entity.GatewayLatencyMs)
				// Existing DO NOTHING semantics: a replay must not overwrite observations.
				changed := v2FieldLog(log.RequestID, &body, &latency)
				inserted, err := repo.createSingle(ctx, db, changed)
				require.NoError(t, err)
				require.False(t, inserted)
				again, err := repo.GetByID(ctx, id)
				require.NoError(t, err)
				require.Equal(t, tc.body, again.BodyBytes)
				require.Equal(t, tc.latency, again.GatewayLatencyMs)
			})
		}
	}
	t.Log("PASS: migration adds nullable BIGINTs; pre-migration history NULL; 4 insert paths x 4 nullable/value cases; SQL/ent reads agree; duplicate replay preserves stored observations")
}
