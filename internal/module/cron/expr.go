package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Expr is a parsed five-field cron expression. Each field is a bitset of the values it
// matches; Matches tests a time at minute precision. The zero value matches nothing —
// always obtain one through Parse.
type Expr struct {
	minute uint64 // bits 0–59
	hour   uint64 // bits 0–23
	dom    uint64 // bits 1–31
	month  uint64 // bits 1–12
	dow    uint64 // bits 0–6 (Sunday = 0; 7 normalizes to 0)

	// domStar / dowStar record whether the field was exactly "*" — the POSIX day rule
	// (see Matches) turns on them, not on the bitsets. A stepped "*/2" is restricted.
	domStar, dowStar bool
}

// fieldSpec is the grammar of one of the five fields.
type fieldSpec struct {
	name     string
	min, max int
	names    map[string]int // 3-letter names, or nil
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

var fieldSpecs = [5]fieldSpec{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day-of-month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: monthNames},
	// 0–7 so both spellings of Sunday parse; bit 7 is folded onto bit 0 after parsing.
	{name: "day-of-week", min: 0, max: 7, names: dowNames},
}

// Parse reads a five-field cron expression (minute hour day-of-month month day-of-week).
// Each field is a comma list of `*`, `N`, `N-M`, optionally stepped with `/S` (`*/S`,
// `N-M/S`, and `N/S` meaning N to the field maximum). Months and days of the week also take
// their 3-letter names, case-insensitive; day-of-week accepts both 0 and 7 as Sunday.
// Errors name the offending field, which is what the trigger editor renders.
func Parse(s string) (Expr, error) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) != 5 {
		return Expr{}, fmt.Errorf(
			"a cron expression has 5 fields: minute hour day-of-month month day-of-week (got %d)",
			len(fields))
	}
	var bits [5]uint64
	for i, f := range fields {
		b, err := parseField(f, fieldSpecs[i])
		if err != nil {
			return Expr{}, err
		}
		bits[i] = b
	}
	e := Expr{
		minute:  bits[0],
		hour:    bits[1],
		dom:     bits[2],
		month:   bits[3],
		dow:     bits[4],
		domStar: fields[2] == "*",
		dowStar: fields[4] == "*",
	}
	// Fold 7 (the other Sunday) onto 0.
	if e.dow&(1<<7) != 0 {
		e.dow = (e.dow | 1) &^ (1 << 7)
	}
	return e, nil
}

// parseField parses one comma-separated field into its bitset.
func parseField(field string, spec fieldSpec) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return 0, fmt.Errorf("%s: %q has an empty list entry", spec.name, field)
		}
		b, err := parsePart(part, spec)
		if err != nil {
			return 0, err
		}
		bits |= b
	}
	return bits, nil
}

// parsePart parses one list entry: `*`, `N`, `N-M`, each optionally `/S`-stepped.
func parsePart(part string, spec fieldSpec) (uint64, error) {
	rangePart, step, hadStep := part, 1, false
	if base, stepStr, ok := strings.Cut(part, "/"); ok {
		n, err := strconv.Atoi(stepStr)
		if err != nil {
			return 0, fmt.Errorf("%s: step %q is not a number", spec.name, stepStr)
		}
		if n < 1 {
			return 0, fmt.Errorf("%s: step %d must be at least 1", spec.name, n)
		}
		rangePart, step, hadStep = base, n, true
	}

	var lo, hi int
	switch {
	case rangePart == "*":
		lo, hi = spec.min, spec.max
	case strings.Contains(rangePart, "-"):
		loStr, hiStr, _ := strings.Cut(rangePart, "-")
		var err error
		if lo, err = parseValue(loStr, spec); err != nil {
			return 0, err
		}
		if hi, err = parseValue(hiStr, spec); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, fmt.Errorf("%s: range %q is reversed", spec.name, rangePart)
		}
	default:
		v, err := parseValue(rangePart, spec)
		if err != nil {
			return 0, err
		}
		lo = v
		// A bare value is itself; a stepped bare value (`N/S`, the Vixie reading) runs
		// from N to the field maximum.
		if hadStep {
			hi = spec.max
		} else {
			hi = v
		}
	}

	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << v
	}
	return bits, nil
}

// parseValue parses one numeric value or 3-letter name, range-checked.
func parseValue(s string, spec fieldSpec) (int, error) {
	if spec.names != nil {
		if v, ok := spec.names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		if spec.names != nil {
			return 0, fmt.Errorf("%s: %q is not a number or a 3-letter name", spec.name, s)
		}
		return 0, fmt.Errorf("%s: %q is not a number", spec.name, s)
	}
	if v < spec.min || v > spec.max {
		return 0, fmt.Errorf("%s: %d is out of range %d-%d", spec.name, v, spec.min, spec.max)
	}
	return v, nil
}

// Matches reports whether t's minute is one this expression fires on. The caller passes UTC;
// the expression carries no location of its own. The day-of-month/day-of-week combination
// follows POSIX crontab: a field is unrestricted only when it was written exactly `*`; when
// both are restricted, the day matches when EITHER does; otherwise the restricted one (if
// any) decides alone.
func (e Expr) Matches(t time.Time) bool {
	if e.minute&(1<<t.Minute()) == 0 ||
		e.hour&(1<<t.Hour()) == 0 ||
		e.month&(1<<int(t.Month())) == 0 {
		return false
	}
	domOK := e.dom&(1<<t.Day()) != 0
	dowOK := e.dow&(1<<int(t.Weekday())) != 0
	switch {
	case e.domStar && e.dowStar:
		return true
	case e.domStar:
		return dowOK
	case e.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}
