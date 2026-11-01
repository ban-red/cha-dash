package backups

import (
	"slices"
	"strings"
	"time"
)

// The objectives for schedules the defaults don't name: a month plus a day,
// a year plus two.
const (
	defaultMonthly = 32 * 24 * time.Hour
	defaultYearly  = 367 * 24 * time.Hour
)

// ScheduleObjective is the freshness objective a backup schedule implies
// (decision 7): DefaultDaily for anything that runs every day or more
// often, DefaultWeekly for once a week, and in between the longest gap
// between two runs plus a day (Monday to Friday: 4 days). It reads
// Proxmox's calendar events ("daily", "sun 01:00", "mon..fri 02:00",
// "*-*-01 03:00"), cron's five fields ("0 3 * * 0") and the plain words
// hourly, daily, weekly, monthly and yearly. ok is false when it can't
// tell.
func ScheduleObjective(schedule string) (time.Duration, bool) {
	s := strings.ToLower(strings.TrimSpace(schedule))
	if s == "" {
		return 0, false
	}
	switch s {
	case "minutely", "hourly", "daily", "nightly", "@hourly", "@daily", "@midnight":
		return DefaultDaily, true
	case "weekly", "@weekly":
		return DefaultWeekly, true
	case "monthly", "@monthly":
		return defaultMonthly, true
	case "yearly", "annually", "@yearly", "@annually":
		return defaultYearly, true
	}
	f := strings.Fields(s)
	if len(f) == 5 && isCronField(f[0]) && isCronField(f[1]) {
		return cronObjective(f)
	}
	// A calendar event: [weekdays] [date] [time]. A date with a fixed day of
	// the month runs monthly at most. Proxmox's integration words it with a
	// leading period ("daily 03:00", "weekly sun 05:00").
	var days []int
	weekly := false
	for _, part := range f {
		switch {
		case part == "daily" || part == "hourly" || part == "minutely":
		case part == "weekly":
			weekly = true
		case part == "monthly":
			return defaultMonthly, true
		case part == "yearly" || part == "annually":
			return defaultYearly, true
		case strings.Count(part, "-") == 2 && !strings.HasSuffix(part, "-*"):
			return defaultMonthly, true
		case part[0] >= 'a' && part[0] <= 'z':
			d, ok := weekdays(part)
			if !ok {
				return 0, false
			}
			days = append(days, d...)
		}
	}
	if weekly && len(days) == 0 {
		return DefaultWeekly, true
	}
	return gapObjective(days), true
}

// gapObjective turns the weekdays a schedule runs on into an objective.
func gapObjective(days []int) time.Duration {
	slices.Sort(days)
	days = slices.Compact(days)
	if len(days) == 0 || len(days) == 7 {
		return DefaultDaily
	}
	if len(days) == 1 {
		return DefaultWeekly
	}
	gap := days[0] + 7 - days[len(days)-1]
	for i := 1; i < len(days); i++ {
		gap = max(gap, days[i]-days[i-1])
	}
	if gap <= 1 {
		return DefaultDaily
	}
	return time.Duration(gap+1) * 24 * time.Hour
}

var dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// weekdays reads "mon", "mon,wed", "mon..fri" and "sat,sun" into 0–6.
func weekdays(s string) ([]int, bool) {
	var out []int
	for p := range strings.SplitSeq(s, ",") {
		from, to, isRange := strings.Cut(p, "..")
		a := dayIndex(from)
		if a < 0 {
			return nil, false
		}
		if !isRange {
			out = append(out, a)
			continue
		}
		b := dayIndex(to)
		if b < 0 {
			return nil, false
		}
		for i := a; ; i = (i + 1) % 7 {
			out = append(out, i)
			if i == b {
				break
			}
		}
	}
	return out, true
}

func dayIndex(s string) int {
	if len(s) < 3 {
		return -1
	}
	return slices.Index(dayNames, s[:3])
}

func isCronField(s string) bool {
	return strings.Trim(s, "0123456789*/,-") == ""
}

// cronObjective reads minute hour day-of-month month day-of-week.
func cronObjective(f []string) (time.Duration, bool) {
	if f[3] != "*" {
		return defaultYearly, true
	}
	if f[2] != "*" && !strings.HasPrefix(f[2], "*/") {
		return defaultMonthly, true
	}
	if f[4] == "*" || f[4] == "?" {
		return DefaultDaily, true
	}
	var days []int
	for p := range strings.SplitSeq(f[4], ",") {
		from, to, isRange := strings.Cut(p, "-")
		a, ok := cronDay(from)
		if !ok {
			return 0, false
		}
		if !isRange {
			days = append(days, a)
			continue
		}
		b, ok := cronDay(to)
		if !ok {
			return 0, false
		}
		for i := a; i <= b; i++ {
			days = append(days, i%7)
		}
	}
	return gapObjective(days), true
}

func cronDay(s string) (int, bool) {
	if len(s) != 1 || s[0] < '0' || s[0] > '7' {
		return 0, false
	}
	return int(s[0]-'0') % 7, true
}
