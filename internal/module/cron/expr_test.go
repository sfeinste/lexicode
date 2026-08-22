package cron

import (
	"strings"
	"testing"
	"time"
)

// at is a UTC minute for Matches tests. 2026-08-17 is a Monday.
func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestParseAndMatches(t *testing.T) {
	cases := []struct {
		name string
		expr string
		yes  []string // minutes that match
		no   []string // minutes that do not
	}{
		{
			name: "all stars match every minute",
			expr: "* * * * *",
			yes:  []string{"2026-08-17 00:00", "2026-08-17 23:59", "2026-02-28 12:34"},
		},
		{
			name: "fixed minute and hour",
			expr: "30 9 * * *",
			yes:  []string{"2026-08-17 09:30", "2026-12-01 09:30"},
			no:   []string{"2026-08-17 09:31", "2026-08-17 10:30", "2026-08-17 00:00"},
		},
		{
			name: "the contracts §4 example: weekdays at 09:00",
			expr: "0 9 * * 1-5",
			yes:  []string{"2026-08-17 09:00", "2026-08-21 09:00"},                     // Mon, Fri
			no:   []string{"2026-08-22 09:00", "2026-08-23 09:00", "2026-08-17 09:01"}, // Sat, Sun
		},
		{
			name: "minute list",
			expr: "0,15,30,45 * * * *",
			yes:  []string{"2026-08-17 10:00", "2026-08-17 10:15", "2026-08-17 10:45"},
			no:   []string{"2026-08-17 10:05", "2026-08-17 10:59"},
		},
		{
			name: "minute range",
			expr: "10-12 * * * *",
			yes:  []string{"2026-08-17 03:10", "2026-08-17 03:11", "2026-08-17 03:12"},
			no:   []string{"2026-08-17 03:09", "2026-08-17 03:13"},
		},
		{
			name: "star step",
			expr: "*/15 * * * *",
			yes:  []string{"2026-08-17 08:00", "2026-08-17 08:15", "2026-08-17 08:30", "2026-08-17 08:45"},
			no:   []string{"2026-08-17 08:10", "2026-08-17 08:59"},
		},
		{
			name: "range step",
			expr: "10-20/5 * * * *",
			yes:  []string{"2026-08-17 01:10", "2026-08-17 01:15", "2026-08-17 01:20"},
			no:   []string{"2026-08-17 01:12", "2026-08-17 01:25"},
		},
		{
			name: "bare value step runs to the field maximum (Vixie N/S)",
			expr: "50/5 * * * *",
			yes:  []string{"2026-08-17 01:50", "2026-08-17 01:55"},
			no:   []string{"2026-08-17 01:45", "2026-08-17 01:51"},
		},
		{
			name: "hour range with step and minute zero",
			expr: "0 9-17/4 * * *",
			yes:  []string{"2026-08-17 09:00", "2026-08-17 13:00", "2026-08-17 17:00"},
			no:   []string{"2026-08-17 11:00", "2026-08-17 09:30"},
		},
		{
			name: "day of month",
			expr: "0 0 1,15 * *",
			yes:  []string{"2026-08-01 00:00", "2026-08-15 00:00"},
			no:   []string{"2026-08-02 00:00", "2026-08-31 00:00"},
		},
		{
			name: "month by number and name",
			expr: "0 12 1 JAN,jul *",
			yes:  []string{"2026-01-01 12:00", "2026-07-01 12:00"},
			no:   []string{"2026-02-01 12:00", "2026-12-01 12:00"},
		},
		{
			name: "month name range",
			expr: "0 0 1 mar-may *",
			yes:  []string{"2026-03-01 00:00", "2026-04-01 00:00", "2026-05-01 00:00"},
			no:   []string{"2026-02-01 00:00", "2026-06-01 00:00"},
		},
		{
			name: "day-of-week names",
			expr: "0 9 * * MON,WED",
			yes:  []string{"2026-08-17 09:00", "2026-08-19 09:00"},
			no:   []string{"2026-08-18 09:00", "2026-08-23 09:00"},
		},
		{
			name: "day-of-week 7 is Sunday",
			expr: "0 9 * * 7",
			yes:  []string{"2026-08-23 09:00"},
			no:   []string{"2026-08-17 09:00", "2026-08-22 09:00"},
		},
		{
			name: "day-of-week 0 is Sunday too",
			expr: "0 9 * * 0",
			yes:  []string{"2026-08-23 09:00"},
			no:   []string{"2026-08-17 09:00"},
		},
		{
			name: "day-of-week range crossing into 7",
			expr: "0 9 * * 5-7",
			yes:  []string{"2026-08-21 09:00", "2026-08-22 09:00", "2026-08-23 09:00"}, // Fri Sat Sun
			no:   []string{"2026-08-17 09:00", "2026-08-20 09:00"},
		},
		{
			name: "POSIX rule: dom restricted, dow star — dom decides",
			expr: "0 0 15 * *",
			yes:  []string{"2026-08-15 00:00"}, // a Saturday
			no:   []string{"2026-08-17 00:00"},
		},
		{
			name: "POSIX rule: dow restricted, dom star — dow decides",
			expr: "0 0 * * 1",
			yes:  []string{"2026-08-17 00:00", "2026-08-24 00:00"},
			no:   []string{"2026-08-15 00:00"},
		},
		{
			name: "POSIX rule: both restricted — either matches (OR)",
			expr: "0 0 15 * 1",
			// 2026-08-15 is a Saturday (dom hit); 2026-08-17 a Monday (dow hit).
			yes: []string{"2026-08-15 00:00", "2026-08-17 00:00", "2026-08-24 00:00"},
			no:  []string{"2026-08-16 00:00", "2026-08-18 00:00"},
		},
		{
			name: "a stepped dom is restricted for the POSIX rule",
			expr: "0 0 */2 * 1",
			// dom */2 covers odd days (1,3,5,...31); OR with Mondays.
			yes: []string{"2026-08-15 00:00", "2026-08-17 00:00", "2026-08-24 00:00"},
			no:  []string{"2026-08-16 00:00"}, // an even day, a Sunday
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := Parse(tc.expr)
			if err != nil {
				t.Fatalf("Parse(%q) = %v", tc.expr, err)
			}
			for _, m := range tc.yes {
				if !e.Matches(at(m)) {
					t.Errorf("Parse(%q).Matches(%s) = false, want true", tc.expr, m)
				}
			}
			for _, m := range tc.no {
				if e.Matches(at(m)) {
					t.Errorf("Parse(%q).Matches(%s) = true, want false", tc.expr, m)
				}
			}
		})
	}
}

func TestParseErrorsNameTheSegment(t *testing.T) {
	cases := []struct {
		expr string
		want string // substring the error must carry — always names the offending field
	}{
		{"", "5 fields"},
		{"* * * *", "5 fields: minute hour day-of-month month day-of-week (got 4)"},
		{"* * * * * *", "(got 6)"},
		{"60 * * * *", `minute: 60 is out of range 0-59`},
		{"-1 * * * *", "minute"},
		{"* 24 * * *", `hour: 24 is out of range 0-23`},
		{"* * 0 * *", `day-of-month: 0 is out of range 1-31`},
		{"* * 32 * *", `day-of-month: 32 is out of range 1-31`},
		{"* * * 0 *", `month: 0 is out of range 1-12`},
		{"* * * 13 *", `month: 13 is out of range 1-12`},
		{"* * * * 8", `day-of-week: 8 is out of range 0-7`},
		{"* * * * MONDAY", `day-of-week: "MONDAY" is not a number or a 3-letter name`},
		{"* * * FOO *", `month: "FOO" is not a number or a 3-letter name`},
		{"x * * * *", `minute: "x" is not a number`},
		{"1;2 * * * *", `minute: "1;2" is not a number`},
		{"20-10 * * * *", `minute: range "20-10" is reversed`},
		{"*/0 * * * *", "minute: step 0 must be at least 1"},
		{"*/x * * * *", `minute: step "x" is not a number`},
		{"1,,2 * * * *", "minute"},
		{"5- * * * *", `minute: "" is not a number`},
		{"/5 * * * *", `minute: "" is not a number`},
		{"@daily * * * *", "minute"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			_, err := Parse(tc.expr)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want an error mentioning %q", tc.expr, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse(%q) error = %q, want it to mention %q", tc.expr, err.Error(), tc.want)
			}
		})
	}
}

func TestParseWhitespaceTolerance(t *testing.T) {
	if _, err := Parse("  0   9  *  *  1-5  "); err != nil {
		t.Fatalf("extra whitespace should parse: %v", err)
	}
}
