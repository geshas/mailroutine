// Package schedule computes routine run times. It has no storage dependencies.
//
// Semantics (all results are returned in UTC):
//
//   - Hourly runs at the configured minute of every hour (absolute elapsed time,
//     so a DST change never skips or doubles an hour).
//   - Daily, weekly and monthly runs fire at the configured local wall-clock time
//     in the configured timezone.
//   - If a wall-clock time does not exist on a given day (spring-forward gap),
//     the run happens at the first valid instant after the gap.
//   - If a wall-clock time occurs twice (fall-back overlap), the run happens
//     once, at the first occurrence.
//   - Monthly runs on a day that does not exist in a month (for example the 31st
//     in April, or the 30th/31st in February) run on the last day of that month.
//   - Next is always strictly after the reference time, so missed runs are never
//     replayed: after downtime a routine runs once and then resumes its cadence.
package schedule

import (
	"errors"
	"time"

	"github.com/geshas/mailroutine/pkg/models"
)

// ErrManual is returned by Next for manual-only schedules.
var ErrManual = errors.New("manual schedules have no next run")

// Next returns the first run time strictly after the given instant.
func Next(t models.ScheduleType, cfg models.ScheduleConfig, after time.Time) (time.Time, error) {
	if err := cfg.Validate(t); err != nil {
		return time.Time{}, err
	}
	if t == models.ScheduleManual {
		return time.Time{}, ErrManual
	}
	loc, _ := cfg.Location()
	local := after.In(loc)

	if t == models.ScheduleHourly {
		base := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, loc)
		// Use absolute arithmetic from the hour start so folds behave predictably.
		cand := base.Add(time.Duration(cfg.Minute) * time.Minute)
		for !cand.After(after) {
			cand = cand.Add(time.Hour)
		}
		return cand.UTC(), nil
	}

	h, m, _ := cfg.HourMinute()
	// Look through enough days to cover any weekly/monthly pattern.
	day := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, loc).AddDate(0, 0, -1)
	for i := 0; i < 400; i++ {
		d := day.AddDate(0, 0, i)
		y, mo, dd := d.Date()
		if !matchesDay(t, cfg, y, mo, dd, loc) {
			continue
		}
		cand := wallClock(y, mo, dd, h, m, loc)
		if cand.After(after) {
			return cand.UTC(), nil
		}
	}
	return time.Time{}, errors.New("no upcoming run found")
}

func matchesDay(t models.ScheduleType, cfg models.ScheduleConfig, y int, mo time.Month, d int, loc *time.Location) bool {
	switch t {
	case models.ScheduleWeekly:
		return int(time.Date(y, mo, d, 12, 0, 0, 0, loc).Weekday()) == cfg.Weekday
	case models.ScheduleMonthly:
		want := cfg.DayOfMonth
		if last := daysIn(y, mo); want > last {
			want = last
		}
		return d == want
	}
	return true // daily
}

func daysIn(y int, mo time.Month) int {
	return time.Date(y, mo+1, 0, 12, 0, 0, 0, time.UTC).Day()
}

// wallClock returns the first instant on the given local date whose wall-clock
// time is at or after h:m. This resolves gaps (first instant after the gap)
// and overlaps (first occurrence) deterministically.
func wallClock(y int, mo time.Month, d, h, m int, loc *time.Location) time.Time {
	target := h*60 + m
	nominal := time.Date(y, mo, d, h, m, 0, 0, loc)
	for t := nominal.Add(-4 * time.Hour); ; t = t.Add(time.Minute) {
		ty, tmo, td := t.Date()
		switch {
		case ty > y || (ty == y && tmo > mo) || (ty == y && tmo == mo && td > d):
			return t
		case ty == y && tmo == mo && td == d && t.Hour()*60+t.Minute() >= target:
			return t
		}
	}
}
