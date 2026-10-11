// Package models contains the provider-agnostic domain types shared by the
// MailRoutine engine, schedulers and storage implementations.
package models

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ActionType identifies what a routine does to each matching message.
type ActionType string

const (
	ActionArchive     ActionType = "archive"
	ActionTrash       ActionType = "trash"
	ActionMarkRead    ActionType = "mark_read"
	ActionMarkUnread  ActionType = "mark_unread"
	ActionAddLabel    ActionType = "add_label"
	ActionRemoveLabel ActionType = "remove_label"
)

// ActionTypes lists every supported action.
var ActionTypes = []ActionType{
	ActionArchive, ActionTrash, ActionMarkRead, ActionMarkUnread, ActionAddLabel, ActionRemoveLabel,
}

// Valid reports whether t is a supported action type.
func (t ActionType) Valid() bool {
	for _, a := range ActionTypes {
		if a == t {
			return true
		}
	}
	return false
}

// Destructive reports whether the action moves mail somewhere the user may
// not look (currently only Trash). Destructive actions need explicit confirmation.
func (t ActionType) Destructive() bool { return t == ActionTrash }

// ActionConfig carries action-specific parameters. New actions can add fields.
type ActionConfig struct {
	Label string `json:"label,omitempty"`
}

// Action is the structured form of a routine's action.
type Action struct {
	Type   ActionType   `json:"type"`
	Config ActionConfig `json:"config"`
}

// ScheduleType identifies how often a routine runs.
type ScheduleType string

const (
	ScheduleManual  ScheduleType = "manual"
	ScheduleHourly  ScheduleType = "hourly"
	ScheduleDaily   ScheduleType = "daily"
	ScheduleWeekly  ScheduleType = "weekly"
	ScheduleMonthly ScheduleType = "monthly"
)

// ScheduleTypes lists every supported schedule type.
var ScheduleTypes = []ScheduleType{ScheduleManual, ScheduleHourly, ScheduleDaily, ScheduleWeekly, ScheduleMonthly}

// Valid reports whether t is a supported schedule type.
func (t ScheduleType) Valid() bool {
	for _, s := range ScheduleTypes {
		if s == t {
			return true
		}
	}
	return false
}

// ScheduleConfig holds the time selection for a schedule.
//
//	hourly : Minute (0-59)
//	daily  : Time ("HH:MM")
//	weekly : Weekday (0=Sunday..6=Saturday) and Time
//	monthly: DayOfMonth (1-31) and Time
//
// Timezone is an IANA name; empty means UTC.
type ScheduleConfig struct {
	Timezone   string `json:"timezone,omitempty"`
	Minute     int    `json:"minute,omitempty"`
	Time       string `json:"time,omitempty"`
	Weekday    int    `json:"weekday,omitempty"`
	DayOfMonth int    `json:"day_of_month,omitempty"`
}

// Location resolves the configured timezone.
func (c ScheduleConfig) Location() (*time.Location, error) {
	if c.Timezone == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", c.Timezone)
	}
	return loc, nil
}

// HourMinute parses Time ("HH:MM").
func (c ScheduleConfig) HourMinute() (int, int, error) {
	var h, m int
	if len(c.Time) != 5 || c.Time[2] != ':' {
		return 0, 0, fmt.Errorf("time must be HH:MM")
	}
	if _, err := fmt.Sscanf(c.Time, "%02d:%02d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("time must be a valid HH:MM")
	}
	return h, m, nil
}

// Validate checks the config for the given schedule type.
func (c ScheduleConfig) Validate(t ScheduleType) error {
	errs := ValidationErrors{}
	if _, err := c.Location(); err != nil {
		errs["schedule_config.timezone"] = err.Error()
	}
	switch t {
	case ScheduleManual:
	case ScheduleHourly:
		if c.Minute < 0 || c.Minute > 59 {
			errs["schedule_config.minute"] = "must be between 0 and 59"
		}
	case ScheduleDaily, ScheduleWeekly, ScheduleMonthly:
		if _, _, err := c.HourMinute(); err != nil {
			errs["schedule_config.time"] = err.Error()
		}
		if t == ScheduleWeekly && (c.Weekday < 0 || c.Weekday > 6) {
			errs["schedule_config.weekday"] = "must be between 0 (Sunday) and 6 (Saturday)"
		}
		if t == ScheduleMonthly && (c.DayOfMonth < 1 || c.DayOfMonth > 31) {
			errs["schedule_config.day_of_month"] = "must be between 1 and 31"
		}
	default:
		errs["schedule_type"] = "unsupported schedule type"
	}
	return errs.OrNil()
}

// Routine is a saved, recurring mailbox maintenance task.
type Routine struct {
	ID             string         `json:"id"`
	AccountID      string         `json:"account_id"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	GmailQuery     string         `json:"gmail_query"`
	ActionType     ActionType     `json:"action_type"`
	ActionConfig   ActionConfig   `json:"action_config"`
	ScheduleType   ScheduleType   `json:"schedule_type"`
	ScheduleConfig ScheduleConfig `json:"schedule_config"`
	Enabled        bool           `json:"enabled"`
	// Traceability adds the MailRoutine/<Action> label to every message the routine handles.
	Traceability      bool       `json:"traceability"`
	MaxMessagesPerRun int        `json:"max_messages_per_run"`
	LastRunAt         *time.Time `json:"last_run_at"`
	NextRunAt         *time.Time `json:"next_run_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// Limits applied to user supplied routine fields.
const (
	MaxNameLength        = 120
	MaxDescriptionLength = 1000
	MaxQueryLength       = 2000
	MaxLabelLength       = 225
	// MaxMessagesPerRunLimit is the hard upper bound for a single run.
	MaxMessagesPerRunLimit = 10000
)

// reservedLabels cannot be added or removed through label actions because they
// would bypass the explicit Trash confirmation or cannot be modified by clients.
var reservedLabels = map[string]bool{"TRASH": true, "SPAM": true, "DRAFT": true, "SENT": true, "CHAT": true}

// TraceParentLabel is the parent of all traceability labels.
const TraceParentLabel = "MailRoutine"

var traceNames = map[ActionType]string{
	ActionArchive: "Archive", ActionTrash: "Trash", ActionMarkRead: "Read",
	ActionMarkUnread: "Unread", ActionAddLabel: "AddLabel", ActionRemoveLabel: "RemoveLabel",
}

// TraceLabel is the label added to messages handled by an action, e.g. "MailRoutine/Archive".
// It returns "" for an unknown action.
func TraceLabel(a ActionType) string {
	if n, ok := traceNames[a]; ok {
		return TraceParentLabel + "/" + n
	}
	return ""
}

// TraceLabels lists every traceability label in the order actions are declared.
func TraceLabels() []string {
	out := make([]string, 0, len(ActionTypes))
	for _, a := range ActionTypes {
		out = append(out, TraceLabel(a))
	}
	return out
}

// Action returns the routine's structured action.
func (r Routine) Action() Action { return Action{Type: r.ActionType, Config: r.ActionConfig} }

// Validate checks the routine's user-editable fields.
func (r Routine) Validate() error {
	errs := ValidationErrors{}
	name := strings.TrimSpace(r.Name)
	switch {
	case name == "":
		errs["name"] = "is required"
	case len([]rune(name)) > MaxNameLength:
		errs["name"] = fmt.Sprintf("must be at most %d characters", MaxNameLength)
	}
	if len([]rune(r.Description)) > MaxDescriptionLength {
		errs["description"] = fmt.Sprintf("must be at most %d characters", MaxDescriptionLength)
	}
	if strings.TrimSpace(r.AccountID) == "" {
		errs["account_id"] = "is required"
	}
	q := strings.TrimSpace(r.GmailQuery)
	switch {
	case q == "":
		// An empty query would match the entire mailbox.
		errs["gmail_query"] = "is required (an empty query would match every message)"
	case len([]rune(q)) > MaxQueryLength:
		errs["gmail_query"] = fmt.Sprintf("must be at most %d characters", MaxQueryLength)
	}
	if !r.ActionType.Valid() {
		errs["action_type"] = "unsupported action"
	}
	if r.ActionType == ActionAddLabel || r.ActionType == ActionRemoveLabel {
		label := strings.TrimSpace(r.ActionConfig.Label)
		switch {
		case label == "":
			errs["action_config.label"] = "is required for label actions"
		case len([]rune(label)) > MaxLabelLength:
			errs["action_config.label"] = fmt.Sprintf("must be at most %d characters", MaxLabelLength)
		case strings.ContainsAny(label, "\x00\r\n\t"):
			errs["action_config.label"] = "contains invalid characters"
		case reservedLabels[strings.ToUpper(label)]:
			errs["action_config.label"] = "this system label cannot be modified"
		}
	}
	if r.MaxMessagesPerRun < 1 || r.MaxMessagesPerRun > MaxMessagesPerRunLimit {
		errs["max_messages_per_run"] = fmt.Sprintf("must be between 1 and %d", MaxMessagesPerRunLimit)
	}
	if !r.ScheduleType.Valid() {
		errs["schedule_type"] = "unsupported schedule type"
	} else if err := r.ScheduleConfig.Validate(r.ScheduleType); err != nil {
		if ve, ok := err.(ValidationErrors); ok {
			for k, v := range ve {
				errs[k] = v
			}
		}
	}
	return errs.OrNil()
}

// ValidationErrors maps field names to problems. It implements error.
type ValidationErrors map[string]string

// OrNil returns nil when no problems were recorded.
func (v ValidationErrors) OrNil() error {
	if len(v) == 0 {
		return nil
	}
	return v
}

func (v ValidationErrors) Error() string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+v[k])
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// ConnectionStatus describes the health of a mail account's authorization.
type ConnectionStatus string

const (
	StatusConnected   ConnectionStatus = "connected"
	StatusNeedsReauth ConnectionStatus = "needs_reauth"
)

// ProviderGmail is the only provider implemented in V1.
const ProviderGmail = "gmail"

// Account is a connected mailbox. The refresh token is only ever stored
// encrypted and is never serialized to JSON.
type Account struct {
	ID                    string           `json:"id"`
	Provider              string           `json:"provider"`
	Email                 string           `json:"email"`
	EncryptedRefreshToken string           `json:"-"`
	ConnectionStatus      ConnectionStatus `json:"connection_status"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

// MessageSummary is the metadata MailRoutine reads about a message. Bodies are
// never fetched or stored.
type MessageSummary struct {
	ID      string    `json:"id"`
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
}

// ExecutionStatus is the outcome of a routine run.
type ExecutionStatus string

const (
	ExecRunning ExecutionStatus = "running"
	ExecSuccess ExecutionStatus = "success"
	ExecPartial ExecutionStatus = "partial"
	ExecFailed  ExecutionStatus = "failed"
	ExecAborted ExecutionStatus = "cancelled"
)

// Trigger records why an execution started.
type Trigger string

const (
	TriggerSchedule Trigger = "schedule"
	TriggerManual   Trigger = "manual"
)

// Execution is one run of a routine.
type Execution struct {
	ID             string          `json:"id"`
	RoutineID      string          `json:"routine_id"`
	RoutineName    string          `json:"routine_name,omitempty"`
	StartedAt      time.Time       `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at"`
	Status         ExecutionStatus `json:"status"`
	MatchedCount   int             `json:"matched_count"`
	ProcessedCount int             `json:"processed_count"`
	FailedCount    int             `json:"failed_count"`
	ErrorMessage   string          `json:"error_message,omitempty"`
	DryRun         bool            `json:"dry_run"`
	Trigger        Trigger         `json:"trigger"`
}

// ActionStatus is the outcome for a single message.
type ActionStatus string

const (
	ActionSuccess ActionStatus = "success"
	ActionFailed  ActionStatus = "failed"
	ActionDryRun  ActionStatus = "dry_run"
)

// MessageActionResult records what happened to one message.
type MessageActionResult struct {
	ID          string       `json:"id,omitempty"`
	ExecutionID string       `json:"execution_id,omitempty"`
	MessageID   string       `json:"gmail_message_id"`
	Action      ActionType   `json:"action"`
	Status      ActionStatus `json:"status"`
	Error       string       `json:"error,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
}
