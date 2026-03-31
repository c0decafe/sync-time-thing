package cronexpr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type field struct {
	any     bool
	allowed map[int]struct{}
}

func (f field) contains(value int) bool {
	if f.any {
		return true
	}
	_, ok := f.allowed[value]
	return ok
}

type Expression struct {
	raw          string
	minute       field
	hour         field
	dayOfMonth   field
	month        field
	dayOfWeek    field
	dayWildcard  bool
	weekWildcard bool
}

var monthAliases = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var dayAliases = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

var nextOccurrence = func(expression Expression, ts time.Time) (time.Time, error) {
	return expression.NextAfter(ts)
}

func Parse(value string) (Expression, error) {
	parts := strings.Fields(strings.TrimSpace(value))
	if len(parts) != 5 {
		return Expression{}, fmt.Errorf("cron expression must contain exactly 5 fields")
	}

	minute, minuteAny, err := parseField(parts[0], 0, 59, nil, false)
	if err != nil {
		return Expression{}, fmt.Errorf("parse minute: %w", err)
	}
	hour, hourAny, err := parseField(parts[1], 0, 23, nil, false)
	if err != nil {
		return Expression{}, fmt.Errorf("parse hour: %w", err)
	}
	dayOfMonth, dayWildcard, err := parseField(parts[2], 1, 31, nil, false)
	if err != nil {
		return Expression{}, fmt.Errorf("parse day-of-month: %w", err)
	}
	month, monthAny, err := parseField(parts[3], 1, 12, monthAliases, false)
	if err != nil {
		return Expression{}, fmt.Errorf("parse month: %w", err)
	}
	dayOfWeek, weekWildcard, err := parseField(parts[4], 0, 6, dayAliases, true)
	if err != nil {
		return Expression{}, fmt.Errorf("parse day-of-week: %w", err)
	}

	return Expression{
		raw:          value,
		minute:       minuteField(minuteAny, minute),
		hour:         minuteField(hourAny, hour),
		dayOfMonth:   minuteField(dayWildcard, dayOfMonth),
		month:        minuteField(monthAny, month),
		dayOfWeek:    minuteField(weekWildcard, dayOfWeek),
		dayWildcard:  dayWildcard,
		weekWildcard: weekWildcard,
	}, nil
}

func minuteField(any bool, values map[int]struct{}) field {
	return field{any: any, allowed: values}
}

func parseField(token string, min, max int, aliases map[string]int, sundayWrap bool) (map[int]struct{}, bool, error) {
	if token == "*" {
		return nil, true, nil
	}

	allowed := make(map[int]struct{})
	for _, part := range strings.Split(token, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false, fmt.Errorf("empty field segment")
		}

		step := 1
		base := part
		if strings.Contains(part, "/") {
			pieces := strings.Split(part, "/")
			if len(pieces) != 2 {
				return nil, false, fmt.Errorf("invalid step syntax %q", part)
			}
			base = pieces[0]
			parsedStep, err := strconv.Atoi(pieces[1])
			if err != nil || parsedStep <= 0 {
				return nil, false, fmt.Errorf("invalid step %q", pieces[1])
			}
			step = parsedStep
		}

		rangeStart := min
		rangeEnd := max
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				return nil, false, fmt.Errorf("invalid range %q", base)
			}
			start, err := parseValue(bounds[0], aliases, sundayWrap)
			if err != nil {
				return nil, false, err
			}
			end, err := parseValue(bounds[1], aliases, sundayWrap)
			if err != nil {
				return nil, false, err
			}
			rangeStart, rangeEnd = start, end
		default:
			value, err := parseValue(base, aliases, sundayWrap)
			if err != nil {
				return nil, false, err
			}
			rangeStart, rangeEnd = value, value
		}

		if rangeStart < min || rangeEnd > max || rangeStart > rangeEnd {
			return nil, false, fmt.Errorf("value %d-%d outside %d-%d", rangeStart, rangeEnd, min, max)
		}

		for value := rangeStart; value <= rangeEnd; value += step {
			allowed[value] = struct{}{}
		}
	}

	return allowed, false, nil
}

func parseValue(raw string, aliases map[string]int, sundayWrap bool) (int, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(raw))
	if aliases != nil {
		if value, ok := aliases[trimmed]; ok {
			return value, nil
		}
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", raw)
	}
	if sundayWrap && value == 7 {
		return 0, nil
	}
	return value, nil
}

func (e Expression) Matches(ts time.Time) bool {
	if !e.minute.contains(ts.Minute()) || !e.hour.contains(ts.Hour()) || !e.month.contains(int(ts.Month())) {
		return false
	}

	dayMatch := e.dayOfMonth.contains(ts.Day())
	weekMatch := e.dayOfWeek.contains(int(ts.Weekday()))
	switch {
	case e.dayWildcard && e.weekWildcard:
		return true
	case e.dayWildcard:
		return weekMatch
	case e.weekWildcard:
		return dayMatch
	default:
		return dayMatch || weekMatch
	}
}

func (e Expression) NextAfter(ts time.Time) (time.Time, error) {
	candidate := ts.Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(5, 0, 0)
	for !candidate.After(limit) {
		if e.Matches(candidate) {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("no scheduled occurrence found within 5 years for %q", e.raw)
}

func (e Expression) Between(start, end time.Time, limit int) ([]time.Time, error) {
	if !end.After(start) {
		return nil, nil
	}
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}

	occurrences := make([]time.Time, 0, limit)
	cursor := start
	for len(occurrences) < limit {
		next, err := nextOccurrence(e, cursor)
		if err != nil {
			if len(occurrences) > 0 {
				return occurrences, nil
			}
			return nil, err
		}
		if next.After(end) {
			break
		}
		occurrences = append(occurrences, next)
		cursor = next
	}
	return occurrences, nil
}

func (e Expression) NextN(start time.Time, count int) ([]time.Time, error) {
	if count <= 0 {
		return nil, fmt.Errorf("count must be positive")
	}
	results := make([]time.Time, 0, count)
	cursor := start
	for len(results) < count {
		next, err := e.NextAfter(cursor)
		if err != nil {
			return nil, err
		}
		results = append(results, next)
		cursor = next
	}
	return results, nil
}
