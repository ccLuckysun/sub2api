package service

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

type usageV2CaptureRepo struct {
	UsageLogRepository
	got      *UsageLog
	inserted bool
}

func (r *usageV2CaptureRepo) Create(_ context.Context, log *UsageLog) (bool, error) {
	r.got = log
	return r.inserted, nil
}

type usageV2UserRepo struct {
	UserRepository
	balanceUpdates int
}

func (r *usageV2UserRepo) GetByID(_ context.Context, id int64) (*User, error) {
	return &User{ID: id}, nil
}
func (r *usageV2UserRepo) UpdateBalance(context.Context, int64, float64) error {
	r.balanceUpdates++
	return nil
}

func TestUsageServiceCreateV2NullableFields(t *testing.T) {
	zero, body, latency := int64(0), int64(1<<40), int64(1<<35)
	for _, tc := range []struct {
		name          string
		body, latency *int64
		inserted      bool
	}{
		{"absent", nil, nil, true}, {"zero", &zero, &zero, true}, {"values", &body, &latency, true}, {"duplicate", &body, &latency, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			repo := &usageV2CaptureRepo{inserted: tc.inserted}
			users := &usageV2UserRepo{}
			svc := NewUsageService(repo, users, client, nil)
			mock.ExpectBegin()
			mock.ExpectCommit()
			got, err := svc.Create(context.Background(), CreateUsageLogRequest{UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "v2", Model: "fixture", BodyBytes: tc.body, GatewayLatencyMs: tc.latency})
			require.NoError(t, err)
			require.Same(t, repo.got, got)
			require.Equal(t, tc.body, got.BodyBytes)
			require.Equal(t, tc.latency, got.GatewayLatencyMs)
			require.Zero(t, users.balanceUpdates, "observational fields must not trigger billing")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
