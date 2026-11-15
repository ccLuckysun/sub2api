package monitoring

import (
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// CoverageSample describes the observations available when a request completes.
type CoverageSample struct{ Request, Account, Bytes, User bool }

type coverageCounts struct{ entry, request, account, bytes, user uint64 }

// CoverageToken binds a completion to its entry minute and accumulator. Its
// completion flag is protected by the owner's mutex, making Complete idempotent.
type CoverageToken struct {
	owner     *OpsCoverage
	minute    time.Time
	completed bool
}

// OpsCoverage retains only the current and previous minute, under one mutex.
// Flush publishes the previous minute, including entries that never completed.
// A late completion may revise that published minute until the next rollover;
// older completions are dropped. The owner should call Flush every minute.
type OpsCoverage struct {
	mu             sync.Mutex
	counts         coverageCounts
	minute         time.Time
	previous       coverageCounts
	previousMinute time.Time
	now            func() time.Time
	gauge          *prometheus.GaugeVec
}

// NewOpsCoverage accepts an injectable clock; nil uses time.Now.
func NewOpsCoverage(gauge *prometheus.GaugeVec, now func() time.Time) *OpsCoverage {
	if now == nil {
		now = time.Now
	}
	minute := now().Truncate(time.Minute)
	return &OpsCoverage{minute: minute, previousMinute: minute.Add(-time.Minute), now: now, gauge: gauge}
}

// Entry immediately increments the denominator, even if Finalize never runs.
func (c *OpsCoverage) Entry() *CoverageToken {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roll(c.now().Truncate(time.Minute))
	c.counts.entry++
	return &CoverageToken{owner: c, minute: c.minute}
}

// Complete counts numerators in the entry minute, never in the completion
// minute. Duplicate, nil, or foreign tokens cannot increment numerators.
func (c *OpsCoverage) Complete(token *CoverageToken, sample CoverageSample) {
	if c == nil || token == nil || token.owner != c {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roll(c.now().Truncate(time.Minute))
	if token.completed {
		return
	}
	token.completed = true
	switch {
	case token.minute.Equal(c.minute):
		addCoverageSample(&c.counts, sample)
	case token.minute.Equal(c.previousMinute):
		addCoverageSample(&c.previous, sample)
		c.publish(c.previous)
	default:
		slog.Debug("ops: coverage completion older than previous minute; dropping")
	}
}

// Add is a compatibility helper for an entry and completion at the same instant.
// Gateway lifecycle instrumentation must use Entry at Begin and Complete at
// Finalize instead, so incomplete requests remain visible in the denominator.
func (c *OpsCoverage) Add(sample CoverageSample) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roll(c.now().Truncate(time.Minute))
	c.counts.entry++
	addCoverageSample(&c.counts, sample)
}

func addCoverageSample(counts *coverageCounts, sample CoverageSample) {
	if sample.Request {
		counts.request++
	}
	if sample.Account {
		counts.account++
	}
	if sample.Bytes {
		counts.bytes++
	}
	if sample.User {
		counts.user++
	}
}

// Flush rolls once and publishes the most recently completed minute. Idle
// minutes publish -1; same-minute calls preserve late-completion revisions.
func (c *OpsCoverage) Flush() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roll(c.now().Truncate(time.Minute))
}

func (c *OpsCoverage) roll(minute time.Time) {
	if !minute.After(c.minute) {
		return
	}
	c.previous = c.counts
	if minute.Sub(c.minute) > time.Minute {
		c.previous = coverageCounts{}
	}
	c.previousMinute = minute.Add(-time.Minute)
	c.counts = coverageCounts{}
	c.minute = minute
	c.publish(c.previous)
}

func (c *OpsCoverage) publish(counts coverageCounts) {
	if c.gauge == nil {
		return
	}
	for kind, n := range map[string]uint64{"request": counts.request, "account": counts.account, "bytes": counts.bytes, "user": counts.user} {
		ratio := -1.0
		switch {
		case counts.entry > 0:
			ratio = float64(n) / float64(counts.entry)
		case n > 0:
			slog.Warn("ops: coverage numerator without entry", "kind", kind)
			ratio = 1
		}
		if ratio > 1 {
			ratio = 1
		}
		if ratio < -1 {
			ratio = -1
		}
		c.gauge.WithLabelValues(kind).Set(ratio)
	}
}
