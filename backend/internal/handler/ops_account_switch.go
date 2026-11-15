package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/monitoring"
)

// Called immediately before forwarding, after account selection and admission.
// Only a different account from the last forwarded account is a real switch.
// The existing scheduler counter has its own semantics and is left unchanged.
func markForwardedAccountSwitch(ctx context.Context, previousAccountID *int64, accountID int64) {
	if ctx == nil || ctx.Err() != nil || previousAccountID == nil {
		return
	}
	if *previousAccountID != 0 && *previousAccountID != accountID {
		monitoring.RecorderFromContext(ctx).MarkSwitch()
	}
	*previousAccountID = accountID
}

// Records a switch if the request is still active.
func markConfirmedAccountSwitch(ctx context.Context) {
	if ctx != nil && ctx.Err() == nil {
		monitoring.RecorderFromContext(ctx).MarkSwitch()
	}
}
