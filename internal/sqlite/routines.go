package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/pkg/models"
)

const routineCols = `id, account_id, name, description, gmail_query, action_type, action_config,
	schedule_type, schedule_config, enabled, max_messages_per_run, last_run_at, next_run_at, created_at, updated_at, traceability`

func scanRoutine(r interface{ Scan(...any) error }) (models.Routine, error) {
	var x models.Routine
	var actionCfg, schedCfg string
	var enabled, trace int
	var last, next sql.NullInt64
	var created, updated int64
	err := r.Scan(&x.ID, &x.AccountID, &x.Name, &x.Description, &x.GmailQuery, &x.ActionType, &actionCfg,
		&x.ScheduleType, &schedCfg, &enabled, &x.MaxMessagesPerRun, &last, &next, &created, &updated, &trace)
	if err != nil {
		return x, err
	}
	if err := json.Unmarshal([]byte(actionCfg), &x.ActionConfig); err != nil {
		return x, err
	}
	if err := json.Unmarshal([]byte(schedCfg), &x.ScheduleConfig); err != nil {
		return x, err
	}
	x.Enabled, x.Traceability = enabled == 1, trace == 1
	x.LastRunAt, x.NextRunAt = ptrFromNull(last), ptrFromNull(next)
	x.CreatedAt, x.UpdatedAt = fromMs(created), fromMs(updated)
	return x, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CreateRoutine inserts a routine; the caller supplies NextRunAt.
func (s *Store) CreateRoutine(ctx context.Context, r models.Routine) (models.Routine, error) {
	now := time.Now().UTC()
	r.ID, r.CreatedAt, r.UpdatedAt = security.RandomID(), now, now
	ac, _ := json.Marshal(r.ActionConfig)
	sc, _ := json.Marshal(r.ScheduleConfig)
	_, err := s.db.ExecContext(ctx, `INSERT INTO routines (`+routineCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.AccountID, r.Name, r.Description, r.GmailQuery, r.ActionType, string(ac),
		r.ScheduleType, string(sc), boolInt(r.Enabled), r.MaxMessagesPerRun, msPtr(r.LastRunAt), msPtr(r.NextRunAt), ms(now), ms(now), boolInt(r.Traceability))
	if err != nil {
		return r, err
	}
	return r, nil
}

// UpdateRoutine rewrites the user-editable fields and next run time.
func (s *Store) UpdateRoutine(ctx context.Context, r models.Routine) (models.Routine, error) {
	ac, _ := json.Marshal(r.ActionConfig)
	sc, _ := json.Marshal(r.ScheduleConfig)
	r.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE routines SET account_id=?, name=?, description=?, gmail_query=?, action_type=?, action_config=?,
		schedule_type=?, schedule_config=?, enabled=?, max_messages_per_run=?, next_run_at=?, updated_at=?, traceability=? WHERE id=?`,
		r.AccountID, r.Name, r.Description, r.GmailQuery, r.ActionType, string(ac), r.ScheduleType, string(sc),
		boolInt(r.Enabled), r.MaxMessagesPerRun, msPtr(r.NextRunAt), ms(r.UpdatedAt), boolInt(r.Traceability), r.ID)
	if err := affected(res, err); err != nil {
		return r, err
	}
	return s.GetRoutine(ctx, r.ID)
}

// GetRoutine returns one routine.
func (s *Store) GetRoutine(ctx context.Context, id string) (models.Routine, error) {
	r, err := scanRoutine(s.db.QueryRowContext(ctx, `SELECT `+routineCols+` FROM routines WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// ListRoutines returns routines, optionally for one account, newest first.
func (s *Store) ListRoutines(ctx context.Context, accountID string) ([]models.Routine, error) {
	q, args := `SELECT `+routineCols+` FROM routines`, []any{}
	if accountID != "" {
		q += ` WHERE account_id = ?`
		args = append(args, accountID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Routine{}
	for rows.Next() {
		r, err := scanRoutine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRoutine removes a routine and its history.
func (s *Store) DeleteRoutine(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM routines WHERE id = ?`, id)
	return affected(res, err)
}

// SetRoutineEnabled toggles a routine and sets its next run (nil when disabled or manual).
func (s *Store) SetRoutineEnabled(ctx context.Context, id string, enabled bool, next *time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE routines SET enabled = ?, next_run_at = ?, updated_at = ? WHERE id = ?`,
		boolInt(enabled), msPtr(next), ms(time.Now()), id)
	return affected(res, err)
}

// DueRoutines lists enabled routines whose next run has passed and that are not
// currently leased.
func (s *Store) DueRoutines(ctx context.Context, now, staleBefore time.Time, limit int) ([]models.Routine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+routineCols+` FROM routines
		WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?
		  AND (claim_token IS NULL OR claimed_at < ?)
		ORDER BY next_run_at LIMIT ?`, ms(now), ms(staleBefore), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Routine
	for rows.Next() {
		r, err := scanRoutine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClaimRoutine atomically takes the execution lease. It returns false if
// another run holds a lease younger than staleBefore.
func (s *Store) ClaimRoutine(ctx context.Context, id, token string, now, staleBefore time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE routines SET claim_token = ?, claimed_at = ?
		WHERE id = ? AND (claim_token IS NULL OR claimed_at < ?)`, token, ms(now), id, ms(staleBefore))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Heartbeat refreshes a held lease. It returns false if the lease was lost.
func (s *Store) Heartbeat(ctx context.Context, id, token string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE routines SET claimed_at = ? WHERE id = ? AND claim_token = ?`, ms(now), id, token)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CompleteRun releases the lease and records the run times. A nil next clears
// the next run (manual schedules, or routines disabled mid-run keep nil).
func (s *Store) CompleteRun(ctx context.Context, id, token string, lastRun time.Time, next *time.Time, updateSchedule bool) error {
	q := `UPDATE routines SET claim_token = NULL, claimed_at = NULL, last_run_at = ?`
	args := []any{ms(lastRun)}
	if updateSchedule {
		q += `, next_run_at = ?`
		args = append(args, msPtr(next))
	}
	res, err := s.db.ExecContext(ctx, q+` WHERE id = ? AND claim_token = ?`, append(args, id, token)...)
	return affected(res, err)
}

// ReleaseClaim drops a lease without recording a run.
func (s *Store) ReleaseClaim(ctx context.Context, id, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE routines SET claim_token = NULL, claimed_at = NULL WHERE id = ? AND claim_token = ?`, id, token)
	return err
}

// ReleaseAllClaims clears every lease. Used at startup: this process is the
// only worker, so any lease found then belongs to a previous, dead process.
func (s *Store) ReleaseAllClaims(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE routines SET claim_token = NULL, claimed_at = NULL WHERE claim_token IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// IsClaimed reports whether a routine currently holds a (non-stale) lease.
func (s *Store) IsClaimed(ctx context.Context, id string, staleBefore time.Time) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routines WHERE id = ? AND claim_token IS NOT NULL AND claimed_at >= ?`,
		id, ms(staleBefore)).Scan(&n)
	return n > 0, err
}
