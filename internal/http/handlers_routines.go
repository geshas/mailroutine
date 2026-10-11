package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/geshas/mailroutine/internal/runner"
	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/models"
	"github.com/geshas/mailroutine/pkg/schedule"
)

type routineInput struct {
	AccountID         string                `json:"account_id"`
	Name              string                `json:"name"`
	Description       string                `json:"description"`
	GmailQuery        string                `json:"gmail_query"`
	ActionType        models.ActionType     `json:"action_type"`
	ActionConfig      models.ActionConfig   `json:"action_config"`
	ScheduleType      models.ScheduleType   `json:"schedule_type"`
	ScheduleConfig    models.ScheduleConfig `json:"schedule_config"`
	MaxMessagesPerRun int                   `json:"max_messages_per_run"`
	Enabled           bool                  `json:"enabled"`
	// Traceability labels every handled message with MailRoutine/<Action>. Omitted = on for
	// new routines and unchanged for existing ones.
	Traceability       *bool `json:"traceability"`
	ConfirmDestructive bool  `json:"confirm_destructive"`
}

// routineView adds derived data the UI needs.
type routineView struct {
	models.Routine
	AccountEmail string `json:"account_email"`
	Running      bool   `json:"running"`
	// LastRunHandled is the number of messages handled by the latest finished
	// real run; absent if the routine has never run.
	LastRunHandled *int `json:"last_run_handled"`
}

func (s *Server) view(r *http.Request, rt models.Routine, emails map[string]string, handled map[string]int) routineView {
	running, _ := s.Store.IsClaimed(r.Context(), rt.ID, time.Now().Add(-runner.LeaseStaleAfter))
	v := routineView{Routine: rt, AccountEmail: emails[rt.AccountID], Running: running}
	if n, ok := handled[rt.ID]; ok {
		v.LastRunHandled = &n
	}
	return v
}

// handledByRoutine loads last-run counts; failures only omit the figure.
func (s *Server) handledByRoutine(r *http.Request) map[string]int {
	m, err := s.Store.LastHandled(r.Context())
	if err != nil {
		s.log.Warn("loading last-run counts failed", "error", err)
		return nil
	}
	return m
}

func (s *Server) accountEmails(r *http.Request) map[string]string {
	out := map[string]string{}
	if list, err := s.Store.ListAccounts(r.Context()); err == nil {
		for _, a := range list {
			out[a.ID] = a.Email
		}
	}
	return out
}

func (s *Server) handleListRoutines(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	list, err := s.Store.ListRoutines(r.Context(), r.URL.Query().Get("account_id"))
	if err != nil {
		s.internalError(w, err, "listing routines failed")
		return
	}
	emails, handled := s.accountEmails(r), s.handledByRoutine(r)
	views := make([]routineView, len(list))
	for i, rt := range list {
		views[i] = s.view(r, rt, emails, handled)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

func (s *Server) handleGetRoutine(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	rt, err := s.Store.GetRoutine(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err, "routine")
		return
	}
	writeJSON(w, http.StatusOK, s.view(r, rt, s.accountEmails(r), s.handledByRoutine(r)))
}

// build validates input and produces a routine ready to store.
func (s *Server) build(w http.ResponseWriter, r *http.Request, in routineInput, base models.Routine) (models.Routine, bool) {
	rt := base
	rt.AccountID, rt.Name, rt.Description = in.AccountID, strings.TrimSpace(in.Name), in.Description
	rt.GmailQuery = strings.TrimSpace(in.GmailQuery)
	rt.ActionType, rt.ActionConfig = in.ActionType, in.ActionConfig
	rt.ActionConfig.Label = strings.TrimSpace(rt.ActionConfig.Label)
	if !rt.ActionType.Valid() || (rt.ActionType != models.ActionAddLabel && rt.ActionType != models.ActionRemoveLabel) {
		rt.ActionConfig = models.ActionConfig{} // drop stray config for actions that take none
	}
	rt.ScheduleType, rt.ScheduleConfig = in.ScheduleType, in.ScheduleConfig
	if rt.ScheduleConfig.Timezone == "" {
		rt.ScheduleConfig.Timezone = s.timezone(r)
	}
	rt.MaxMessagesPerRun = in.MaxMessagesPerRun
	if rt.MaxMessagesPerRun == 0 {
		rt.MaxMessagesPerRun = s.Config.DefaultMaxMessages
	}
	rt.Enabled = in.Enabled
	switch {
	case in.Traceability != nil:
		rt.Traceability = *in.Traceability
	case base.ID == "":
		rt.Traceability = true // new routines trace by default
	}

	if err := rt.Validate(); err != nil {
		var ve models.ValidationErrors
		errors.As(err, &ve)
		writeError(w, http.StatusBadRequest, "validation_failed", "the routine is not valid", ve)
		return rt, false
	}
	acc, err := s.Store.GetAccount(r.Context(), rt.AccountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "the routine is not valid",
			map[string]string{"account_id": "unknown Gmail account"})
		return rt, false
	}
	if rt.ActionType.Destructive() && rt.Enabled && !in.ConfirmDestructive {
		writeError(w, http.StatusBadRequest, "confirmation_required",
			"moving mail to Trash needs explicit confirmation (confirm_destructive=true)", nil)
		return rt, false
	}
	if rt.Enabled && acc.ConnectionStatus != models.StatusConnected {
		writeError(w, http.StatusConflict, "account_needs_reauth", "reconnect this Gmail account before enabling routines for it", nil)
		return rt, false
	}
	next, err := nextRun(rt, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "the routine is not valid", map[string]string{"schedule_config": err.Error()})
		return rt, false
	}
	rt.NextRunAt = next
	return rt, true
}

// nextRun returns the next scheduled run for an enabled, non-manual routine.
func nextRun(rt models.Routine, after time.Time) (*time.Time, error) {
	if !rt.Enabled || rt.ScheduleType == models.ScheduleManual {
		return nil, nil
	}
	n, err := schedule.Next(rt.ScheduleType, rt.ScheduleConfig, after)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *Server) handleCreateRoutine(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	var in routineInput
	if !decode(w, r, &in) {
		return
	}
	rt, ok := s.build(w, r, in, models.Routine{})
	if !ok {
		return
	}
	created, err := s.Store.CreateRoutine(r.Context(), rt)
	if err != nil {
		s.internalError(w, err, "creating routine failed")
		return
	}
	s.log.Info("routine created", "routine_id", created.ID, "action", string(created.ActionType), "enabled", created.Enabled)
	writeJSON(w, http.StatusCreated, s.view(r, created, s.accountEmails(r), nil))
}

func (s *Server) handleUpdateRoutine(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	existing, err := s.Store.GetRoutine(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err, "routine")
		return
	}
	var in routineInput
	if !decode(w, r, &in) {
		return
	}
	rt, ok := s.build(w, r, in, existing)
	if !ok {
		return
	}
	updated, err := s.Store.UpdateRoutine(r.Context(), rt)
	if err != nil {
		s.storeError(w, err, "routine")
		return
	}
	s.log.Info("routine updated", "routine_id", updated.ID, "enabled", updated.Enabled)
	writeJSON(w, http.StatusOK, s.view(r, updated, s.accountEmails(r), s.handledByRoutine(r)))
}

func (s *Server) handleDeleteRoutine(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	id := r.PathValue("id")
	if busy, _ := s.Store.IsClaimed(r.Context(), id, time.Now().Add(-runner.LeaseStaleAfter)); busy {
		writeError(w, http.StatusConflict, "routine_running", "this routine is running; disable it and try again when it finishes", nil)
		return
	}
	if err := s.Store.DeleteRoutine(r.Context(), id); err != nil {
		s.storeError(w, err, "routine")
		return
	}
	s.log.Info("routine deleted", "routine_id", id)
	w.WriteHeader(http.StatusNoContent)
}

type confirmInput struct {
	ConfirmDestructive bool `json:"confirm_destructive"`
	DryRun             bool `json:"dry_run"`
}

func (s *Server) handleEnableRoutine(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	rt, err := s.Store.GetRoutine(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err, "routine")
		return
	}
	var in confirmInput
	if !decode(w, r, &in) {
		return
	}
	if rt.ActionType.Destructive() && !in.ConfirmDestructive {
		writeError(w, http.StatusBadRequest, "confirmation_required",
			"moving mail to Trash needs explicit confirmation (confirm_destructive=true)", nil)
		return
	}
	acc, err := s.Store.GetAccount(r.Context(), rt.AccountID)
	if err != nil || acc.ConnectionStatus != models.StatusConnected {
		writeError(w, http.StatusConflict, "account_needs_reauth", "reconnect this Gmail account before enabling routines for it", nil)
		return
	}
	rt.Enabled = true
	next, err := nextRun(rt, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "the routine's schedule is not valid", nil)
		return
	}
	if err := s.Store.SetRoutineEnabled(r.Context(), rt.ID, true, next); err != nil {
		s.storeError(w, err, "routine")
		return
	}
	s.log.Info("routine enabled", "routine_id", rt.ID)
	s.handleGetRoutine(w, r, sqlite.Session{})
}

func (s *Server) handleDisableRoutine(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	id := r.PathValue("id")
	if err := s.Store.SetRoutineEnabled(r.Context(), id, false, nil); err != nil {
		s.storeError(w, err, "routine")
		return
	}
	s.log.Info("routine disabled", "routine_id", id)
	s.handleGetRoutine(w, r, sqlite.Session{})
}

func (s *Server) handleRunRoutine(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	rt, err := s.Store.GetRoutine(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err, "routine")
		return
	}
	var in confirmInput
	if !decode(w, r, &in) {
		return
	}
	if rt.ActionType.Destructive() && !in.DryRun && !in.ConfirmDestructive {
		writeError(w, http.StatusBadRequest, "confirmation_required",
			"moving mail to Trash needs explicit confirmation (confirm_destructive=true)", nil)
		return
	}
	exec, err := s.Runner.Start(r.Context(), rt.ID, models.TriggerManual, in.DryRun)
	switch {
	case err == nil:
		s.log.Info("manual run requested", "routine_id", rt.ID, "execution_id", exec.ID, "dry_run", in.DryRun)
		exec.RoutineName = rt.Name
		writeJSON(w, http.StatusAccepted, exec)
	case errors.Is(err, runner.ErrBusy):
		writeError(w, http.StatusConflict, "routine_running", "this routine is already running", nil)
	case errors.Is(err, runner.ErrAtCapacity):
		writeError(w, http.StatusTooManyRequests, "at_capacity", err.Error(), nil)
	default:
		s.internalError(w, err, "starting run failed")
	}
}

type previewInput struct {
	AccountID  string `json:"account_id"`
	GmailQuery string `json:"gmail_query"`
	Limit      int    `json:"limit"`
}

func (s *Server) handlePreviewSaved(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	rt, err := s.Store.GetRoutine(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err, "routine")
		return
	}
	var in previewInput
	if !decode(w, r, &in) {
		return
	}
	s.preview(w, r, rt.AccountID, rt.GmailQuery, in.Limit)
}

func (s *Server) handlePreviewUnsaved(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	var in previewInput
	if !decode(w, r, &in) {
		return
	}
	s.preview(w, r, in.AccountID, in.GmailQuery, in.Limit)
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request, accountID, query string, limit int) {
	query = strings.TrimSpace(query)
	if query == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "the preview is not valid", map[string]string{"gmail_query": "is required"})
		return
	}
	if len([]rune(query)) > models.MaxQueryLength {
		writeError(w, http.StatusBadRequest, "validation_failed", "the preview is not valid", map[string]string{"gmail_query": "is too long"})
		return
	}
	acc, err := s.Store.GetAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "the preview is not valid", map[string]string{"account_id": "unknown Gmail account"})
		return
	}
	if limit <= 0 {
		limit = 25
	}
	pv, err := s.Runner.Preview(r.Context(), acc, query, limit)
	if err != nil {
		s.providerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pv)
}
