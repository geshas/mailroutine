package schedule

import (
	"errors"
	"testing"
	"time"

	"github.com/geshas/mailroutine/pkg/models"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	ny := "America/New_York"
	cases := []struct {
		name  string
		typ   models.ScheduleType
		cfg   models.ScheduleConfig
		after string
		want  string
	}{
		{"hourly later this hour", models.ScheduleHourly, models.ScheduleConfig{Minute: 30}, "2026-03-10T10:10:00Z", "2026-03-10T10:30:00Z"},
		{"hourly next hour", models.ScheduleHourly, models.ScheduleConfig{Minute: 30}, "2026-03-10T10:30:00Z", "2026-03-10T11:30:00Z"},
		{"daily today", models.ScheduleDaily, models.ScheduleConfig{Time: "03:00"}, "2026-03-10T01:00:00Z", "2026-03-10T03:00:00Z"},
		{"daily tomorrow", models.ScheduleDaily, models.ScheduleConfig{Time: "03:00"}, "2026-03-10T03:00:00Z", "2026-03-11T03:00:00Z"},
		{"daily timezone", models.ScheduleDaily, models.ScheduleConfig{Time: "03:00", Timezone: ny}, "2026-01-10T00:00:00Z", "2026-01-10T08:00:00Z"},
		{"weekly", models.ScheduleWeekly, models.ScheduleConfig{Time: "09:00", Weekday: 1}, "2026-03-10T00:00:00Z", "2026-03-16T09:00:00Z"}, // 2026-03-10 is Tuesday
		{"weekly same day later", models.ScheduleWeekly, models.ScheduleConfig{Time: "09:00", Weekday: 2}, "2026-03-10T00:00:00Z", "2026-03-10T09:00:00Z"},
		{"monthly", models.ScheduleMonthly, models.ScheduleConfig{Time: "06:00", DayOfMonth: 15}, "2026-03-20T00:00:00Z", "2026-04-15T06:00:00Z"},
		{"monthly feb 31 clamps to 28", models.ScheduleMonthly, models.ScheduleConfig{Time: "06:00", DayOfMonth: 31}, "2026-02-01T00:00:00Z", "2026-02-28T06:00:00Z"},
		{"monthly feb leap clamps to 29", models.ScheduleMonthly, models.ScheduleConfig{Time: "06:00", DayOfMonth: 30}, "2028-02-01T00:00:00Z", "2028-02-29T06:00:00Z"},
		{"monthly 31 skips to next month 31", models.ScheduleMonthly, models.ScheduleConfig{Time: "06:00", DayOfMonth: 31}, "2026-01-31T06:00:00Z", "2026-02-28T06:00:00Z"},
		{"monthly april 31 clamps to 30", models.ScheduleMonthly, models.ScheduleConfig{Time: "06:00", DayOfMonth: 31}, "2026-04-01T00:00:00Z", "2026-04-30T06:00:00Z"},
		// DST spring forward 2026-03-08 in New York: 02:00 -> 03:00 EDT.
		{"daily in spring-forward gap", models.ScheduleDaily, models.ScheduleConfig{Time: "02:30", Timezone: ny}, "2026-03-08T00:00:00Z", "2026-03-08T07:00:00Z"},
		// 02:30 EST the day before is 07:30Z; the day after is 02:30 EDT = 06:30Z.
		{"daily after gap day", models.ScheduleDaily, models.ScheduleConfig{Time: "02:30", Timezone: ny}, "2026-03-08T08:00:00Z", "2026-03-09T06:30:00Z"},
		// DST fall back 2026-11-01: 01:30 happens twice; run at first (EDT = 05:30Z) only.
		{"daily in fold first occurrence", models.ScheduleDaily, models.ScheduleConfig{Time: "01:30", Timezone: ny}, "2026-11-01T00:00:00Z", "2026-11-01T05:30:00Z"},
		{"daily after fold does not repeat", models.ScheduleDaily, models.ScheduleConfig{Time: "01:30", Timezone: ny}, "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"},
		{"hourly across DST keeps cadence", models.ScheduleHourly, models.ScheduleConfig{Minute: 0, Timezone: ny}, "2026-03-08T06:30:00Z", "2026-03-08T07:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Next(tc.typ, tc.cfg, ts(tc.after))
			if err != nil {
				t.Fatal(err)
			}
			if want := ts(tc.want); !got.Equal(want) {
				t.Fatalf("got %s want %s", got.Format(time.RFC3339), tc.want)
			}
			if got.Location() != time.UTC {
				t.Fatalf("result must be UTC")
			}
		})
	}
}

func TestManualHasNoNext(t *testing.T) {
	_, err := Next(models.ScheduleManual, models.ScheduleConfig{}, time.Now())
	if !errors.Is(err, ErrManual) {
		t.Fatalf("got %v", err)
	}
}

func TestInvalidConfigRejected(t *testing.T) {
	bad := []struct {
		typ models.ScheduleType
		cfg models.ScheduleConfig
	}{
		{models.ScheduleHourly, models.ScheduleConfig{Minute: 60}},
		{models.ScheduleDaily, models.ScheduleConfig{Time: "25:00"}},
		{models.ScheduleDaily, models.ScheduleConfig{Time: "9:00"}},
		{models.ScheduleWeekly, models.ScheduleConfig{Time: "09:00", Weekday: 7}},
		{models.ScheduleMonthly, models.ScheduleConfig{Time: "09:00", DayOfMonth: 0}},
		{models.ScheduleDaily, models.ScheduleConfig{Time: "09:00", Timezone: "Mars/Olympus"}},
	}
	for _, b := range bad {
		if _, err := Next(b.typ, b.cfg, time.Now()); err == nil {
			t.Errorf("expected error for %v %+v", b.typ, b.cfg)
		}
	}
}

// After downtime the next run is computed from "now", so exactly one run is
// due rather than a replay of every missed slot.
func TestNoReplayOfMissedRuns(t *testing.T) {
	cfg := models.ScheduleConfig{Time: "03:00"}
	now := ts("2026-03-20T12:00:00Z") // a week after the last scheduled run
	next, _ := Next(models.ScheduleDaily, cfg, now)
	if !next.Equal(ts("2026-03-21T03:00:00Z")) {
		t.Fatalf("got %s", next)
	}
}

// Samoa skipped 2011-12-30 entirely; the next daily run must still land on a real day.
func TestDayThatDoesNotExist(t *testing.T) {
	loc, err := time.LoadLocation("Pacific/Apia")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	after := time.Date(2011, 12, 29, 12, 0, 0, 0, loc)
	got, err := Next(models.ScheduleDaily, models.ScheduleConfig{Time: "10:00", Timezone: "Pacific/Apia"}, after)
	if err != nil {
		t.Fatal(err)
	}
	l := got.In(loc)
	if !got.After(after) || l.Day() != 31 || l.Hour() != 10 {
		t.Fatalf("got %s", l)
	}
}
