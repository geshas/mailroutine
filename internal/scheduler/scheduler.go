// Package scheduler periodically starts routines that are due.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/geshas/mailroutine/internal/runner"
	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/models"
)

// Scheduler polls the database for due routines.
type Scheduler struct {
	Store    *sqlite.Store
	Runner   *runner.Runner
	Interval time.Duration
	Logger   *slog.Logger
	Now      func() time.Time
	// Maintenance runs occasionally (session pruning, history retention).
	Maintenance func(ctx context.Context)
	// MaintenanceInterval defaults to one hour.
	MaintenanceInterval time.Duration
}

// Run blocks until ctx is cancelled, ticking every Interval (and once at start).
func (s *Scheduler) Run(ctx context.Context) {
	if s.Now == nil {
		s.Now = time.Now
	}
	log := s.Logger.With("component", "scheduler")
	log.Info("scheduler started", "interval", s.Interval.String())
	s.Tick(ctx)
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	if s.MaintenanceInterval <= 0 {
		s.MaintenanceInterval = time.Hour
	}
	maint := time.NewTicker(s.MaintenanceInterval)
	defer maint.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("scheduler stopped")
			return
		case <-t.C:
			s.Tick(ctx)
		case <-maint.C:
			if s.Maintenance != nil {
				s.Maintenance(ctx)
			}
		}
	}
}

// Tick starts every due routine once. A failure to start one routine never
// prevents the others from starting.
func (s *Scheduler) Tick(ctx context.Context) {
	if s.Now == nil {
		s.Now = time.Now
	}
	log := s.Logger.With("component", "scheduler")
	if err := s.Runner.RecoverStale(ctx); err != nil {
		log.Error("recovering stale runs failed", "error", err)
	}
	now := s.Now()
	due, err := s.Store.DueRoutines(ctx, now, now.Add(-runner.LeaseStaleAfter), 100)
	if err != nil {
		log.Error("querying due routines failed", "error", err)
		return
	}
	for _, r := range due {
		_, err := s.Runner.Start(ctx, r.ID, models.TriggerSchedule, false)
		switch {
		case err == nil:
		case err == runner.ErrAtCapacity:
			log.Debug("at capacity; remaining routines wait for the next tick", "routine_id", r.ID)
			return
		case err == runner.ErrBusy:
		default:
			log.Error("starting routine failed", "routine_id", r.ID, "error", err)
		}
	}
}
