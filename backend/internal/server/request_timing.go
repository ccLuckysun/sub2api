package server

import (
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
)

// ProvideRequestTimingEnricher builds the independent bounded timing patch
// worker from configuration. It is deliberately not part of the usage-record
// worker pool: timing enrichment must never delay or fail billing.
func ProvideRequestTimingEnricher(db *sql.DB, cfg *config.Config) *requesttiming.Enricher {
	var timing config.RequestTimingConfig
	if cfg != nil {
		timing = cfg.RequestTiming
	}
	return requesttiming.NewEnricher(db, requesttiming.EnricherConfig{
		Enabled:       timing.EnrichEnabled,
		Workers:       timing.Workers,
		QueueSize:     timing.QueueSize,
		BatchSize:     timing.BatchSize,
		FlushInterval: time.Duration(timing.FlushIntervalMS) * time.Millisecond,
		SQLTimeout:    time.Duration(timing.SQLTimeoutMS) * time.Millisecond,
		MaxAttempts:   timing.MaxAttempts,
	})
}
