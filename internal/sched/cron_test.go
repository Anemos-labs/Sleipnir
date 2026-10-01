package sched

import (
	"testing"
	"time"
)

func TestCronNext(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct{ expr, from, want string }{
		{"* * * * *", "2026-10-01 12:00", "2026-10-01 12:01"},
		{"*/15 * * * *", "2026-10-01 12:14", "2026-10-01 12:15"},
		{"*/15 * * * *", "2026-10-01 12:45", "2026-10-01 13:00"},
		{"30 9 * * *", "2026-10-01 09:30", "2026-10-02 09:30"},
		{"0 9 * * 1-5", "2026-10-02 10:00", "2026-10-05 09:00"}, // Friday after nine: Monday
		{"0 0 1 * *", "2026-10-15 00:00", "2026-11-01 00:00"},
		{"0 0 29 2 *", "2026-10-01 00:00", "2028-02-29 00:00"},
		{"10-30/10 8,20 * * *", "2026-10-01 08:20", "2026-10-01 08:30"},
		{"@hourly", "2026-12-31 23:59", "2027-01-01 00:00"},
		{"@weekly", "2026-10-01 00:00", "2026-10-04 00:00"},
		{"0 12 * * 7", "2026-10-01 00:00", "2026-10-04 12:00"},   // 7 is Sunday
		{"0 12 13 * 5", "2026-10-01 00:00", "2026-10-02 12:00"},  // both day fields restricted: either matches (Friday the 2nd)
		{"5/20 * * * *", "2026-10-01 00:06", "2026-10-01 00:25"}, // from 5 on, every 20
	} {
		c, err := ParseCron(tc.expr)
		if err != nil {
			t.Fatalf("%s: %v", tc.expr, err)
		}
		got, ok := c.Next(at(tc.from))
		if !ok || !got.Equal(at(tc.want)) {
			t.Errorf("%s after %s: got %v %v, want %s", tc.expr, tc.from, got, ok, tc.want)
		}
	}
	c, _ := ParseCron("0 0 31 2 *")
	if _, ok := c.Next(at("2026-10-01 00:00")); ok {
		t.Error("February 31st never comes")
	}
}

func TestCronRefusesWhatItCannotRead(t *testing.T) {
	for _, bad := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "a * * * *", "*/0 * * * *", "5-1 * * * *", "@yearly"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
