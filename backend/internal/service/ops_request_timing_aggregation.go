package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

func (s *OpsAggregationService) startTimingAggregation() {
	r, ok := s.opsRepo.(RequestTimingRepository)
	if !ok {
		return
	}
	base, cancel := context.WithCancel(context.Background())
	s.timingMu.Lock()
	s.timingCancel = cancel
	s.timingMu.Unlock()
	s.timingWG.Add(1)
	go func() {
		defer s.timingWG.Done()
		defer cancel()
		timer := time.NewTimer(0)
		defer timer.Stop()
		var lastCatchupLog time.Time
		for {
			select {
			case <-s.stopCh:
				return
			case <-timer.C:
			}
			nextDelay := time.Minute
			if s.cfg == nil || (s.cfg.Ops.Enabled && s.cfg.RequestTiming.AggregateEnabled) {
				ctx, cancel := context.WithTimeout(base, 20*time.Second)
				if s.isMonitoringEnabled(ctx) {
					if err := r.AggregateRequestTiming(ctx, time.Now()); err != nil {
						logger.LegacyPrintf("service.request_timing", "timing aggregation failed: %v", err)
						nextDelay = 5 * time.Second
					}
					if pendingRepo, ok := r.(interface {
						TimingAggregationPending(context.Context, time.Time) (bool, error)
					}); ok {
						check, checkCancel := context.WithTimeout(base, time.Second)
						pending, err := pendingRepo.TimingAggregationPending(check, time.Now())
						checkCancel()
						if err != nil {
							logger.LegacyPrintf("service.request_timing", "timing scheduling state unavailable: %v", err)
							nextDelay = 5 * time.Second
						} else if pending {
							nextDelay = 2 * time.Second
							if time.Since(lastCatchupLog) >= time.Minute {
								logger.LegacyPrintf("service.request_timing", "timing aggregation backlog remains; scheduling bounded catch-up in 2s")
								lastCatchupLog = time.Now()
							}
						}
					}
				}
				cancel()
			}
			timer.Reset(nextDelay)
		}
	}()
}
