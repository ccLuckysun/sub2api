package service

import (
	"context"
	"time"
)

func (s *UsageService) GetTimingUsage(ctx context.Context, id int64, at *time.Time) (*UsageLog, error) {
	if at != nil {
		if repo, ok := s.usageRepo.(interface {
			GetByIDAndCreatedAt(context.Context, int64, time.Time) (*UsageLog, error)
		}); ok {
			return repo.GetByIDAndCreatedAt(ctx, id, *at)
		}
	}
	return s.GetByID(ctx, id)
}
