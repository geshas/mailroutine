package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/models"
)

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	d, err := s.Store.Dashboard(r.Context())
	if err != nil {
		s.internalError(w, err, "dashboard query failed")
		return
	}
	var next any
	if d.NextRun != nil {
		next = map[string]any{"routine_id": d.NextRun.RoutineID, "routine_name": d.NextRun.RoutineName, "at": d.NextRun.At}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_runs":         d.TotalRuns,
		"last_run":           d.LastRun,
		"next_run":           next,
		"emails_handled":     d.EmailsHandled,
		"enabled_routines":   d.EnabledRoutines,
		"connected_accounts": d.ConnectedAccounts,
		"recent_runs":        d.RecentRuns,
	})
}

func intParam(r *http.Request, name string, def, lo, hi int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return max(lo, min(v, hi))
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	q := r.URL.Query()
	f := sqlite.ExecutionFilter{
		RoutineID: q.Get("routine_id"),
		Status:    models.ExecutionStatus(q.Get("status")),
		Limit:     intParam(r, "limit", 25, 1, 100),
		Offset:    intParam(r, "offset", 0, 0, 1<<30),
	}
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_filter", name+" must be an RFC 3339 timestamp", map[string]string{name: "invalid timestamp"})
				return
			}
			*dst = &t
		}
	}
	if f.Status != "" {
		switch f.Status {
		case models.ExecRunning, models.ExecSuccess, models.ExecPartial, models.ExecFailed, models.ExecAborted:
		default:
			writeError(w, http.StatusBadRequest, "invalid_filter", "unknown status filter", map[string]string{"status": "unknown value"})
			return
		}
	}
	items, total, err := s.Store.ListExecutions(r.Context(), f)
	if err != nil {
		s.internalError(w, err, "listing runs failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": f.Limit, "offset": f.Offset})
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	x, err := s.Store.GetExecution(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err, "run")
		return
	}
	actions, err := s.Store.ListMessageActions(r.Context(), x.ID, intParam(r, "actions_limit", 200, 1, 1000), intParam(r, "actions_offset", 0, 0, 1<<30))
	if err != nil {
		s.internalError(w, err, "listing message actions failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": x, "actions": actions})
}

// timezone returns the configured display/default timezone name.
func (s *Server) timezone(r *http.Request) string {
	if v, err := s.Store.GetSetting(r.Context(), settingTZ); err == nil {
		return v
	}
	return s.Config.Timezone
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	cfg := s.Auth.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone":                     s.timezone(r),
		"version":                      s.Version,
		"license":                      licenseName,
		"repository_url":               repoURL,
		"documentation_url":            docsURL,
		"app_url":                      s.Config.AppURL.String(),
		"google_redirect_url":          s.Config.GoogleRedirectURL,
		"default_max_messages_per_run": s.Config.DefaultMaxMessages,
		"scheduler_interval_seconds":   int(s.Config.SchedulerInterval.Seconds()),
		"session_idle_timeout_seconds": int(cfg.IdleTimeout.Seconds()),
		"admin_username":               cfg.Username,
		"server_time":                  time.Now().UTC(),
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	var body struct {
		Timezone *string `json:"timezone"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Timezone != nil {
		if _, err := time.LoadLocation(*body.Timezone); err != nil || *body.Timezone == "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "invalid settings", map[string]string{"timezone": "unknown IANA timezone"})
			return
		}
		if err := s.Store.SetSetting(r.Context(), settingTZ, *body.Timezone); err != nil {
			s.internalError(w, err, "saving settings failed")
			return
		}
		if err := s.applyTimezone(r, *body.Timezone); err != nil {
			s.internalError(w, err, "applying timezone to routines failed")
			return
		}
	}
	s.handleGetSettings(w, r, sqlite.Session{})
}

func (s *Server) storeError(w http.ResponseWriter, err error, what string) {
	if err == sqlite.ErrNotFound {
		writeError(w, http.StatusNotFound, "not_found", what+" not found", nil)
		return
	}
	s.internalError(w, err, what+" query failed")
}

// applyTimezone makes every routine follow the global timezone and recomputes
// their next runs, so "Routine schedules use this timezone" is literally true.
func (s *Server) applyTimezone(r *http.Request, tz string) error {
	routines, err := s.Store.ListRoutines(r.Context(), "")
	if err != nil {
		return err
	}
	for _, rt := range routines {
		if rt.ScheduleConfig.Timezone == tz {
			continue
		}
		rt.ScheduleConfig.Timezone = tz
		if rt.NextRunAt, err = nextRun(rt, time.Now()); err != nil {
			return err
		}
		if _, err := s.Store.UpdateRoutine(r.Context(), rt); err != nil {
			return err
		}
	}
	return nil
}
