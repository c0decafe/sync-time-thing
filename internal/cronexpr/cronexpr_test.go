package cronexpr

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseAndMatches(t *testing.T) {
	expr, err := Parse("*/15 9-17 * JAN,MAR MON-FRI")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	match := time.Date(2026, time.January, 5, 9, 15, 0, 0, time.UTC) // Monday
	if !expr.Matches(match) {
		t.Fatal("expected expression to match")
	}

	nonMatch := time.Date(2026, time.February, 5, 9, 15, 0, 0, time.UTC)
	if expr.Matches(nonMatch) {
		t.Fatal("expected expression not to match")
	}
}

func TestDayMatchingUsesCronSemantics(t *testing.T) {
	expr, err := Parse("0 12 1 * MON")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	firstOfMonth := time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)
	monday := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)

	if !expr.Matches(firstOfMonth) || !expr.Matches(monday) {
		t.Fatal("expected OR semantics across day-of-month and day-of-week")
	}
}

func TestNextAfterBetweenAndNextN(t *testing.T) {
	expr, err := Parse("0 */6 * * *")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	start := time.Date(2026, time.March, 30, 1, 2, 0, 0, time.UTC)
	next, err := expr.NextAfter(start)
	if err != nil {
		t.Fatalf("NextAfter returned error: %v", err)
	}
	if next.Hour() != 6 || next.Minute() != 0 {
		t.Fatalf("unexpected next occurrence: %s", next)
	}

	between, err := expr.Between(start, time.Date(2026, time.March, 30, 18, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatalf("Between returned error: %v", err)
	}
	if len(between) != 3 {
		t.Fatalf("expected 3 occurrences, got %d", len(between))
	}

	nextThree, err := expr.NextN(start, 3)
	if err != nil {
		t.Fatalf("NextN returned error: %v", err)
	}
	if len(nextThree) != 3 || !nextThree[0].Equal(between[0]) {
		t.Fatal("unexpected next N results")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []string{
		"",
		"* * * *",
		"61 * * * *",
		"* */0 * * *",
		"* * * * FUNDAY",
		"* * * * * *",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := Parse(input)
			if err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestBetweenAndNextNValidation(t *testing.T) {
	expr, err := Parse("0 0 * * *")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if _, err := expr.Between(time.Now(), time.Now().Add(time.Hour), 0); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("unexpected Between error: %v", err)
	}
	if _, err := expr.NextN(time.Now(), 0); err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("unexpected NextN error: %v", err)
	}
}

func TestInternalHelpers(t *testing.T) {
	values, any, err := parseField("*", 0, 59, nil, false)
	if err != nil || !any || values != nil {
		t.Fatalf("unexpected wildcard parse result: values=%v any=%v err=%v", values, any, err)
	}
	values, any, err = parseField("1-5/2,7", 0, 10, nil, false)
	if err != nil || any {
		t.Fatalf("unexpected stepped field parse result: values=%v any=%v err=%v", values, any, err)
	}
	if _, _, err := parseField("1,,2", 0, 10, nil, false); err == nil {
		t.Fatal("expected empty segment to fail")
	}
	if _, _, err := parseField("5-1", 0, 10, nil, false); err == nil {
		t.Fatal("expected inverted range to fail")
	}
	if value, err := parseValue("7", dayAliases, true); err != nil || value != 0 {
		t.Fatalf("expected sunday wrap to normalize to zero, got value=%d err=%v", value, err)
	}
}

func TestNoOccurrencePaths(t *testing.T) {
	expr, err := Parse("0 0 31 2 *")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if _, err := expr.NextAfter(time.Date(2026, time.March, 30, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("expected impossible schedule to fail")
	}
	if occurrences, err := expr.Between(time.Date(2026, time.March, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, time.March, 29, 0, 0, 0, 0, time.UTC), 5); err != nil || len(occurrences) != 0 {
		t.Fatalf("unexpected Between result: occurrences=%v err=%v", occurrences, err)
	}
	if _, err := expr.NextN(time.Date(2026, time.March, 30, 0, 0, 0, 0, time.UTC), 1); err == nil {
		t.Fatal("expected NextN to fail for an impossible schedule")
	}
}

func TestParseAdditionalFieldErrors(t *testing.T) {
	tests := []string{
		"0 0 0 * *",
		"0 0 * FOO *",
		"0 0 * * BOGUS",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := Parse(input); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestParseFieldAdditionalErrors(t *testing.T) {
	if _, _, err := parseField("*/2/3", 0, 59, nil, false); err == nil {
		t.Fatal("expected invalid step syntax to fail")
	}
	if _, _, err := parseField("1-2-3", 0, 59, nil, false); err == nil {
		t.Fatal("expected invalid range syntax to fail")
	}
	if _, _, err := parseField("BOGUS-2", 1, 12, monthAliases, false); err == nil {
		t.Fatal("expected invalid range start to fail")
	}
	if _, _, err := parseField("1-BOGUS", 1, 12, monthAliases, false); err == nil {
		t.Fatal("expected invalid range end to fail")
	}
}

func TestBetweenHandlesLaterLookupFailure(t *testing.T) {
	original := nextOccurrence
	t.Cleanup(func() { nextOccurrence = original })

	now := time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
	calls := 0
	nextOccurrence = func(Expression, time.Time) (time.Time, error) {
		calls++
		if calls == 1 {
			return now.Add(time.Minute), nil
		}
		return time.Time{}, errors.New("boom")
	}

	occurrences, err := (Expression{}).Between(now, now.Add(time.Hour), 3)
	if err != nil {
		t.Fatalf("Between returned error: %v", err)
	}
	if len(occurrences) != 1 || !occurrences[0].Equal(now.Add(time.Minute)) {
		t.Fatalf("unexpected Between result: %v", occurrences)
	}
}

func TestBetweenReturnsErrorWhenNoOccurrenceExists(t *testing.T) {
	expr, err := Parse("0 0 31 2 *")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if _, err := expr.Between(time.Date(2026, time.March, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, time.March, 31, 0, 0, 0, 0, time.UTC), 5); err == nil {
		t.Fatal("expected Between to return an error when no occurrence exists")
	}
}
