//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRequestTimingAlertLockEventAndEvaluationCommitAtomically(t *testing.T) {
	ctx := context.Background()
	r := NewOpsRepository(integrationDB).(*opsRepository)
	rule, err := r.CreateAlertRule(ctx, &service.OpsAlertRule{Name: fmt.Sprintf("timing-atomic-%d", time.Now().UnixNano()), MetricType: "upload_ms_p90", Operator: ">", Threshold: 8000, Severity: "P2", WindowMinutes: 1, SustainedMinutes: 5, NotifyEmail: false, Enabled: false})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM ops_alert_events WHERE rule_id=$1`, rule.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM ops_alert_rules WHERE id=$1`, rule.ID)
	})
	now := time.Now().UTC()
	value, coverage := 9000.0, 1.0
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- r.WithRequestTimingAlertRuleLock(ctx, rule.ID, func(ctx context.Context, tx service.RequestTimingAlertTx) error {
				active, err := tx.GetActiveAlertEvent(ctx, rule.ID)
				if err != nil {
					return err
				}
				if active == nil {
					if _, err := tx.CreateAlertEvent(ctx, &service.OpsAlertEvent{RuleID: rule.ID, Status: service.OpsAlertStatusFiring, Severity: "P2", Title: "Local integration fixture", MetricValue: &value, FiredAt: now, CreatedAt: now, EmailSent: false}); err != nil {
						return err
					}
				}
				return tx.SaveRequestTimingAlertState(ctx, &service.RequestTimingAlertState{RuleID: rule.ID, EvaluatedBucket: now.Truncate(time.Minute), State: service.TimingAlertStateBreached, SampleCount: 30, Coverage: &coverage, MetricValue: &value, UpdatedAt: now})
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM ops_alert_events WHERE rule_id=$1 AND status='firing'`, rule.ID).Scan(&count))
	require.Equal(t, 1, count, "two replicas must not create duplicate firing events")
	states, err := r.ListRequestTimingAlertStates(ctx, []int64{rule.ID})
	require.NoError(t, err)
	require.Equal(t, service.TimingAlertStateBreached, states[rule.ID].State)
	rollback := errors.New("intentional rollback")
	err = r.WithRequestTimingAlertRuleLock(ctx, rule.ID, func(ctx context.Context, tx service.RequestTimingAlertTx) error {
		active, err := tx.GetActiveAlertEvent(ctx, rule.ID)
		if err != nil {
			return err
		}
		if active == nil {
			return errors.New("missing active event")
		}
		if err := tx.UpdateAlertEventStatus(ctx, active.ID, service.OpsAlertStatusResolved, &now); err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	active, err := r.GetActiveAlertEvent(ctx, rule.ID)
	require.NoError(t, err)
	require.NotNil(t, active)
	require.False(t, active.EmailSent)
}
