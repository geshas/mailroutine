package models

import (
	"strings"
	"testing"
	"time"
)

func TestScheduleConfigValidate(t *testing.T) {
	cases := []struct {
		name  string
		typ   ScheduleType
		cfg   ScheduleConfig
		field string // expected error field, "" for valid
	}{
		{"manual ok", ScheduleManual, ScheduleConfig{}, ""},
		{"hourly ok", ScheduleHourly, ScheduleConfig{Minute: 59}, ""},
		{"hourly bad", ScheduleHourly, ScheduleConfig{Minute: -1}, "schedule_config.minute"},
		{"daily ok", ScheduleDaily, ScheduleConfig{Time: "23:59"}, ""},
		{"daily bad format", ScheduleDaily, ScheduleConfig{Time: "9am"}, "schedule_config.time"},
		{"daily bad value", ScheduleDaily, ScheduleConfig{Time: "24:00"}, "schedule_config.time"},
		{"weekly bad day", ScheduleWeekly, ScheduleConfig{Time: "01:00", Weekday: 9}, "schedule_config.weekday"},
		{"monthly bad day", ScheduleMonthly, ScheduleConfig{Time: "01:00", DayOfMonth: 32}, "schedule_config.day_of_month"},
		{"bad tz", ScheduleDaily, ScheduleConfig{Time: "01:00", Timezone: "Nope/Zone"}, "schedule_config.timezone"},
		{"unknown type", ScheduleType("x"), ScheduleConfig{}, "schedule_type"},
	}
	for _, c := range cases {
		err := c.cfg.Validate(c.typ)
		if c.field == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		ve, ok := err.(ValidationErrors)
		if !ok || ve[c.field] == "" {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
}

func TestLocationAndHourMinute(t *testing.T) {
	loc, err := ScheduleConfig{}.Location()
	if err != nil || loc != time.UTC {
		t.Fatal(loc, err)
	}
	loc, err = ScheduleConfig{Timezone: "Europe/Paris"}.Location()
	if err != nil || loc.String() != "Europe/Paris" {
		t.Fatal(loc, err)
	}
	if _, err := (ScheduleConfig{Timezone: "bad"}).Location(); err == nil {
		t.Fatal("bad tz accepted")
	}
	h, m, err := ScheduleConfig{Time: "07:05"}.HourMinute()
	if err != nil || h != 7 || m != 5 {
		t.Fatal(h, m, err)
	}
	for _, bad := range []string{"", "7:05", "07-05", "ab:cd", "25:00", "10:60"} {
		if _, _, err := (ScheduleConfig{Time: bad}).HourMinute(); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestRoutineValidateLimitsAndMessages(t *testing.T) {
	long := strings.Repeat("x", 3000)
	cases := map[string]func(*Routine){
		"name":                 func(r *Routine) { r.Name = long },
		"description":          func(r *Routine) { r.Description = long },
		"gmail_query":          func(r *Routine) { r.GmailQuery = long },
		"action_config.label":  func(r *Routine) { r.ActionType = ActionRemoveLabel; r.ActionConfig.Label = "" },
		"schedule_config.time": func(r *Routine) { r.ScheduleConfig.Time = "bad" },
	}
	for field, mut := range cases {
		r := valid()
		mut(&r)
		ve, _ := r.Validate().(ValidationErrors)
		if ve[field] == "" {
			t.Errorf("%s not reported: %v", field, ve)
		}
	}
	for _, label := range []string{long, "a\nb", "SPAM", "sent", "Draft", "chat"} {
		r := valid()
		r.ActionType = ActionAddLabel
		r.ActionConfig.Label = label
		if r.Validate() == nil {
			t.Errorf("label %q accepted", label[:min(len(label), 10)])
		}
	}
	r := valid()
	r.ActionType = ActionAddLabel
	r.ActionConfig.Label = "Receipts/2026"
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if a := r.Action(); a.Type != ActionAddLabel || a.Config.Label != "Receipts/2026" {
		t.Fatalf("%+v", a)
	}
}

func TestValidationErrorsString(t *testing.T) {
	var none ValidationErrors
	if none.OrNil() != nil {
		t.Fatal("empty must be nil")
	}
	e := ValidationErrors{"b": "two", "a": "one"}
	if got := e.Error(); got != "validation failed: a: one; b: two" {
		t.Fatal(got)
	}
	if ScheduleType("nope").Valid() || ActionType("nope").Valid() {
		t.Fatal("invalid enums accepted")
	}
	for _, s := range ScheduleTypes {
		if !s.Valid() {
			t.Fatal(s)
		}
	}
}

func TestTraceLabels(t *testing.T) {
	want := map[ActionType]string{
		ActionArchive: "MailRoutine/Archive", ActionTrash: "MailRoutine/Trash", ActionMarkRead: "MailRoutine/Read",
		ActionMarkUnread: "MailRoutine/Unread", ActionAddLabel: "MailRoutine/AddLabel", ActionRemoveLabel: "MailRoutine/RemoveLabel",
	}
	for a, w := range want {
		if got := TraceLabel(a); got != w {
			t.Errorf("%s: %q", a, got)
		}
	}
	if TraceLabel("bogus") != "" {
		t.Fatal("unknown action must have no label")
	}
	all := TraceLabels()
	if len(all) != len(ActionTypes) || all[0] != "MailRoutine/Archive" {
		t.Fatal(all)
	}
}
