package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Partition-safe detail lookup: id alone may not be globally unique on a future
// time-partitioned deployment. The normal table still uses its primary index.
func (r *usageLogRepository) GetByIDAndCreatedAt(ctx context.Context, id int64, at time.Time) (*service.UsageLog, error) {
	rows, err := r.sql.QueryContext(ctx, "SELECT "+usageLogSelectColumns+" FROM usage_logs WHERE id=$1 AND created_at=$2", id, at.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrUsageLogNotFound
	}
	log, err := scanUsageLog(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return log, nil
}
