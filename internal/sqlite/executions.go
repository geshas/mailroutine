package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/pkg/models"
)

const execCols = `e.id, e.routine_id, COALESCE(r.name, ''), e.started_at, e.finished_at, e.status,
	e.matched_count, e.processed_count, e.failed_count, e.error_message, e.dry_run, e.trigger`

const execFrom = ` FROM executions e LEFT JOIN routines r ON r.id = e.routine_id`

func scanExecution(r interface{ Scan(...any) error }) (models.Execution, error) {
	var x models.Execution
	var started int64
	var finished sql.NullInt64
	var dry int
	err := r.Scan(&x.ID, &x.RoutineID, &x.RoutineName, &started, &finished, &x.Status,
		&x.MatchedCount, &x.ProcessedCount, &x.FailedCount, &x.ErrorMessage, &dry, &x.Trigger)
	x.StartedAt, x.FinishedAt, x.DryRun = fromMs(started), ptrFromNull(finished), dry == 1
	return x, err
}

// CreateExecution starts a run record in the running state.
func (s *Store) CreateExecution(ctx context.Context, routineID string, trigger models.Trigger, dryRun bool, now time.Time) (models.Execution, error) {
	x := models.Execution{ID: security.RandomID(), RoutineID: routineID, StartedAt: now.UTC(), Status: models.ExecRunning,
		Trigger: trigger, DryRun: dryRun}
	_, err := s.db.ExecContext(ctx, `INSERT INTO executions (id, routine_id, started_at, status, dry_run, trigger) VALUES (?,?,?,?,?,?)`,
		x.ID, x.RoutineID, ms(x.StartedAt), x.Status, boolInt(dryRun), x.Trigger)
	return x, err
}

// UpdateExecutionProgress records live counters.
func (s *Store) UpdateExecutionProgress(ctx context.Context, id string, matched, processed, failed int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE executions SET matched_count=?, processed_count=?, failed_count=? WHERE id=?`,
		matched, processed, failed, id)
	return err
}

// FinishExecution writes the final state.
func (s *Store) FinishExecution(ctx context.Context, id string, status models.ExecutionStatus, matched, processed, failed int, errMsg string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE executions SET finished_at=?, status=?, matched_count=?, processed_count=?, failed_count=?, error_message=? WHERE id=?`,
		ms(now), status, matched, processed, failed, errMsg, id)
	return err
}

// FailRunningExecutions marks leftovers from a crashed process as failed.
func (s *Store) FailRunningExecutions(ctx context.Context, reason string, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE executions SET status='failed', finished_at=?, error_message=? WHERE status='running'`,
		ms(now), reason)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FailStaleExecutions fails running executions of routines whose lease went stale.
func (s *Store) FailStaleExecutions(ctx context.Context, staleBefore time.Time, reason string, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE executions SET status='failed', finished_at=?, error_message=?
		WHERE status='running' AND routine_id IN (SELECT id FROM routines WHERE claim_token IS NULL OR claimed_at < ?)`,
		ms(now), reason, ms(staleBefore))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetExecution returns one run.
func (s *Store) GetExecution(ctx context.Context, id string) (models.Execution, error) {
	x, err := scanExecution(s.db.QueryRowContext(ctx, `SELECT `+execCols+execFrom+` WHERE e.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// ExecutionFilter narrows ListExecutions.
type ExecutionFilter struct {
	RoutineID string
	Status    models.ExecutionStatus
	// From (inclusive) and To (exclusive) bound started_at; nil means unbounded.
	From, To *time.Time
	Limit    int
	Offset   int
}

// ListExecutions returns runs newest first plus the total matching count.
func (s *Store) ListExecutions(ctx context.Context, f ExecutionFilter) ([]models.Execution, int, error) {
	where, args := ` WHERE 1=1`, []any{}
	if f.RoutineID != "" {
		where += ` AND e.routine_id = ?`
		args = append(args, f.RoutineID)
	}
	if f.Status != "" {
		where += ` AND e.status = ?`
		args = append(args, f.Status)
	}
	if f.From != nil {
		where += ` AND e.started_at >= ?`
		args = append(args, ms(*f.From))
	}
	if f.To != nil {
		where += ` AND e.started_at < ?`
		args = append(args, ms(*f.To))
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+execFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 {
		f.Limit = 25
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+execCols+execFrom+where+` ORDER BY e.started_at DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []models.Execution{}
	for rows.Next() {
		x, err := scanExecution(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

// InsertMessageActions stores per-message results in one short transaction.
func (s *Store) InsertMessageActions(ctx context.Context, executionID string, results []models.MessageActionResult) error {
	if len(results) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO message_actions (execution_id, gmail_message_id, action, status, error, created_at) VALUES (?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, r := range results {
			if _, err := stmt.ExecContext(ctx, executionID, r.MessageID, r.Action, r.Status, r.Error, ms(r.CreatedAt)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListMessageActions returns an execution's results, failures first.
func (s *Store) ListMessageActions(ctx context.Context, executionID string, limit, offset int) ([]models.MessageActionResult, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, execution_id, gmail_message_id, action, status, error, created_at
		FROM message_actions WHERE execution_id = ?
		ORDER BY (status = 'failed') DESC, id LIMIT ? OFFSET ?`, executionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.MessageActionResult{}
	for rows.Next() {
		var r models.MessageActionResult
		var id, created int64
		if err := rows.Scan(&id, &r.ExecutionID, &r.MessageID, &r.Action, &r.Status, &r.Error, &created); err != nil {
			return nil, err
		}
		r.ID, r.CreatedAt = strconv.FormatInt(id, 10), fromMs(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneHistory deletes per-message rows and finished executions older than cutoff.
func (s *Store) PruneHistory(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for _, q := range []string{
		`DELETE FROM message_actions WHERE created_at < ?`,
		`DELETE FROM executions WHERE finished_at IS NOT NULL AND finished_at < ?`,
	} {
		res, err := s.db.ExecContext(ctx, q, ms(cutoff))
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// NextRun identifies the soonest scheduled run.
type NextRun struct {
	RoutineID   string
	RoutineName string
	At          time.Time
}

// Dashboard holds aggregated statistics.
type Dashboard struct {
	TotalRuns         int
	EmailsHandled     int
	EnabledRoutines   int
	ConnectedAccounts int
	NextRun           *NextRun
	LastRun           *models.Execution
	RecentRuns        []models.Execution
}

// Dashboard computes the statistics shown on the dashboard. Dry runs are not
// counted as runs or handled emails.
func (s *Store) Dashboard(ctx context.Context) (Dashboard, error) {
	var d Dashboard
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM executions WHERE dry_run = 0`).Scan(&d.TotalRuns); err != nil {
		return d, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_actions ma JOIN executions e ON e.id = ma.execution_id
		WHERE ma.status = 'success' AND e.dry_run = 0`).Scan(&d.EmailsHandled); err != nil {
		return d, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routines WHERE enabled = 1`).Scan(&d.EnabledRoutines); err != nil {
		return d, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE connection_status = 'connected'`).Scan(&d.ConnectedAccounts); err != nil {
		return d, err
	}
	var nr NextRun
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, next_run_at FROM routines
		WHERE enabled = 1 AND next_run_at IS NOT NULL ORDER BY next_run_at LIMIT 1`).Scan(&nr.RoutineID, &nr.RoutineName, &at)
	switch {
	case err == nil:
		nr.At = fromMs(at)
		d.NextRun = &nr
	case !errors.Is(err, sql.ErrNoRows):
		return d, err
	}
	recent, _, err := s.ListExecutions(ctx, ExecutionFilter{Limit: 10})
	if err != nil {
		return d, err
	}
	d.RecentRuns = recent
	for i := range recent {
		if recent[i].Status != models.ExecRunning && !recent[i].DryRun {
			d.LastRun = &recent[i]
			break
		}
	}
	return d, nil
}

// LastHandled returns, per routine, the number of messages successfully handled
// by its most recent finished (non-dry) run. Routines that never ran are absent.
func (s *Store) LastHandled(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.routine_id, e.processed_count FROM executions e
		JOIN (SELECT routine_id, MAX(started_at) AS m FROM executions
		      WHERE dry_run = 0 AND status <> 'running' GROUP BY routine_id) l
		  ON l.routine_id = e.routine_id AND e.started_at = l.m
		WHERE e.dry_run = 0 AND e.status <> 'running'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
