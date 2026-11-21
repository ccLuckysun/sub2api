package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/monitoring"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestProvideOpsSampleWriterDisabled(t *testing.T) {
	require.Nil(t, provideOpsSampleWriter(nil, nil))
	cfg := &config.Config{}
	require.Nil(t, provideOpsSampleWriter(cfg, nil))
	cfg.Monitoring.OpsMetricsEnabled = true
	require.Nil(t, provideOpsSampleWriter(cfg, nil))
	cfg.Monitoring.OpsSamplesEnabled = true
	writer := provideOpsSampleWriter(cfg, nil)
	require.NotNil(t, writer)
	writer.Stop()
	writer.Stop()
}

func TestProvideOpsSampleSinkNil(t *testing.T) {
	require.Nil(t, provideOpsSampleSink(nil))
	cfg := &config.Config{}
	cfg.Monitoring.OpsMetricsEnabled = true
	cfg.Monitoring.OpsSamplesEnabled = true
	writer := provideOpsSampleWriter(cfg, nil)
	defer writer.Stop()
	require.Same(t, writer, provideOpsSampleSink(writer))
}

func TestProvideAppStateProbe(t *testing.T) {
	require.Nil(t, provideAppStateProbe(nil, nil, nil))
	require.Nil(t, provideAppStateProbe(&monitoring.RegistryAndMetrics{}, nil, nil))

	instruments, err := monitoring.ProvideConfiguredRegistryAndMetrics(&config.Config{
		Monitoring: config.MonitoringConfig{MetricsEnabled: true, OpsMetricsEnabled: true},
	})
	require.NoError(t, err)

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	mock.ExpectQuery(`SELECT count\(\*\), COALESCE\(sum\(concurrency\), 0\) FROM accounts`).
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(7, 42))
	mock.ExpectQuery(`SELECT COALESCE\(concurrency_queue_depth, 0\) FROM ops_system_metrics`).
		WillReturnRows(sqlmock.NewRows([]string{"depth"}).AddRow(3))

	probe := provideAppStateProbe(instruments, db, rdb)
	require.NotNil(t, probe)
	snap := probe.Probe(context.Background())
	require.True(t, snap.RedisUp)
	require.EqualValues(t, 7, snap.AccountsActive)
	require.EqualValues(t, 42, snap.AccountsConcurrencyCapacity)
	require.EqualValues(t, 3, snap.ConcurrencyQueueDepth)
	require.NoError(t, mock.ExpectationsWereMet())

	// Missing ops_system_metrics / closed Redis degrades gracefully without panicking.
	mr.Close()
	mock.ExpectQuery(`SELECT count\(\*\), COALESCE\(sum\(concurrency\), 0\) FROM accounts`).
		WillReturnError(errors.New("db hiccup"))
	mock.ExpectQuery(`SELECT COALESCE\(concurrency_queue_depth, 0\) FROM ops_system_metrics`).
		WillReturnError(sql.ErrNoRows)

	snapDown := probe.Probe(context.Background())
	require.False(t, snapDown.RedisUp)
	require.Zero(t, snapDown.AccountsActive)
	require.Zero(t, snapDown.AccountsConcurrencyCapacity)
	require.Zero(t, snapDown.ConcurrencyQueueDepth)
	require.NoError(t, mock.ExpectationsWereMet())
}
