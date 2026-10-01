// Package sched runs the agent on a schedule: a job is a goal, a model and a cron expression, kept in a JSON file; the daemon
// (cmd/sleipnir/daemon.go) starts a headless `sleipnir run` for every job that is due. This file is the cron expression: five
// fields (minute hour day-of-month month day-of-week), each "*", a number, a range "a-b", a list "a,b", and "/n" steps ("*/15",
// "10-30/5"). Day-of-week is 0-6 with 0 Sunday (7 is Sunday too); a day matches when both day fields match, or, if both are
// restricted, when either does, as in cron. "@hourly", "@daily" and "@weekly" are shorthands. Times are in the location given.
package sched

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed expression.
type Cron struct {
	min, hour, dom, mon, dow uint64 // bit sets
	domStar, dowStar         bool
}

var shorthand = map[string]string{"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@weekly": "0 0 * * 0"}

// ParseCron parses an expression.
func ParseCron(expr string) (Cron, error) {
	expr = strings.TrimSpace(expr)
	if s, ok := shorthand[expr]; ok {
		expr = s
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Cron{}, fmt.Errorf("cron %q: want five fields (minute hour day-of-month month day-of-week) or @hourly, @daily, @weekly", expr)
	}
	var c Cron
	var err error
	spans := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	sets := [5]*uint64{&c.min, &c.hour, &c.dom, &c.mon, &c.dow}
	names := [5]string{"minute", "hour", "day-of-month", "month", "day-of-week"}
	for i, field := range f {
		if *sets[i], err = parseField(field, spans[i][0], spans[i][1]); err != nil {
			return Cron{}, fmt.Errorf("cron %q: %s: %w", expr, names[i], err)
		}
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday
		c.dow = c.dow&^(1<<7) | 1
	}
	c.domStar, c.dowStar = f[2][0] == '*', f[4][0] == '*'
	return c, nil
}

func parseField(s string, lo, hi int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(s, ",") {
		step := 1
		if a, b, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(b)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("bad step %q", b)
			}
			part, step = a, n
		}
		from, to := lo, hi
		if part != "*" {
			a, b, isRange := strings.Cut(part, "-")
			var err error
			if from, err = strconv.Atoi(a); err != nil {
				return 0, fmt.Errorf("bad value %q", a)
			}
			to = from
			if isRange {
				if to, err = strconv.Atoi(b); err != nil {
					return 0, fmt.Errorf("bad value %q", b)
				}
			} else if step > 1 {
				to = hi // "5/10" means from 5 on, every 10
			}
		}
		if from < lo || to > hi || from > to {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := from; v <= to; v += step {
			set |= 1 << v
		}
	}
	return set, nil
}

func (c Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<t.Day()) != 0
	dow := c.dow&(1<<int(t.Weekday())) != 0
	if !c.domStar && !c.dowStar {
		return dom || dow
	}
	return dom && dow
}

// Next is the first minute after t that the expression names, in t's location; false when there is none within five years
// (an expression such as "0 0 31 2 *").
func (c Cron) Next(t time.Time) (time.Time, bool) {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case c.mon&(1<<int(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
		case !c.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
		case c.hour&(1<<t.Hour()) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
		case c.min&(1<<t.Minute()) == 0:
			t = t.Add(time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}
