package api

import (
	"encoding/json"
	"sort"
	"testing"
	"time"
)

func TestIsoMillisEmitsExactlyThreeFractionalDigits(t *testing.T) {
	tm, err := time.Parse(time.RFC3339Nano, "2026-04-17T15:40:00.123456789Z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, marshalErr := isoValue(tm).MarshalJSON()
	if marshalErr != nil {
		t.Fatalf("MarshalJSON: %v", marshalErr)
	}

	want := `"2026-04-17T15:40:00.123Z"`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestIsoMillisPadsAWholeSecondToThreeZeros(t *testing.T) {
	tm, err := time.Parse(time.RFC3339Nano, "2026-04-17T15:40:00Z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, marshalErr := isoValue(tm).MarshalJSON()
	if marshalErr != nil {
		t.Fatalf("MarshalJSON: %v", marshalErr)
	}

	want := `"2026-04-17T15:40:00.000Z"`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestIsoMillisPadsATenthToThreeDigits(t *testing.T) {
	tm, err := time.Parse(time.RFC3339Nano, "2026-04-17T15:40:00.1Z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, marshalErr := isoValue(tm).MarshalJSON()
	if marshalErr != nil {
		t.Fatalf("MarshalJSON: %v", marshalErr)
	}

	want := `"2026-04-17T15:40:00.100Z"`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestIsoMillisCoercesANonUTCValueToUTC(t *testing.T) {
	tm, err := time.Parse(time.RFC3339Nano, "2026-04-17T17:40:00+02:00")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, marshalErr := isoValue(tm).MarshalJSON()
	if marshalErr != nil {
		t.Fatalf("MarshalJSON: %v", marshalErr)
	}

	want := `"2026-04-17T15:40:00.000Z"`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestIsoMillisTruncatesRatherThanRoundsSubMilliseconds(t *testing.T) {
	tm, err := time.Parse(time.RFC3339Nano, "2026-04-17T15:40:00.9999Z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, marshalErr := isoValue(tm).MarshalJSON()
	if marshalErr != nil {
		t.Fatalf("MarshalJSON: %v", marshalErr)
	}

	want := `"2026-04-17T15:40:00.999Z"`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestIsoMillisOutputIsLexicographicallySortable(t *testing.T) {
	base, err := time.Parse(time.RFC3339Nano, "2026-04-17T15:40:59.999Z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	times := []time.Time{
		base,
		base.Add(time.Millisecond),
		base.Add(2 * time.Minute),
	}

	marshaled := make([]string, 0, len(times))
	for _, tm := range times {
		got, marshalErr := isoValue(tm).MarshalJSON()
		if marshalErr != nil {
			t.Fatalf("MarshalJSON: %v", marshalErr)
		}
		marshaled = append(marshaled, string(got))
	}

	if !sort.StringsAreSorted(marshaled) {
		t.Fatalf("expected sorted, got %v", marshaled)
	}
}

func TestIsoPtrMarshalsNilAsNull(t *testing.T) {
	type wrapper struct {
		At *isoMillis `json:"at"`
	}

	got, err := json.Marshal(wrapper{At: isoPtr(nil)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	want := `{"at":null}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestIsoPtrMarshalsAValueAsATimestamp(t *testing.T) {
	type wrapper struct {
		At *isoMillis `json:"at"`
	}

	tm, err := time.Parse(time.RFC3339Nano, "2026-04-17T15:40:00Z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, marshalErr := json.Marshal(wrapper{At: isoPtr(&tm)})
	if marshalErr != nil {
		t.Fatalf("Marshal: %v", marshalErr)
	}

	want := `{"at":"2026-04-17T15:40:00.000Z"}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestIsoPtrDoesNotAliasItsArgument(t *testing.T) {
	tm, err := time.Parse(time.RFC3339Nano, "2026-04-17T15:40:00Z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	p := isoPtr(&tm)

	other, otherErr := time.Parse(time.RFC3339Nano, "2030-01-01T00:00:00Z")
	if otherErr != nil {
		t.Fatalf("parse: %v", otherErr)
	}
	tm = other

	got, marshalErr := p.MarshalJSON()
	if marshalErr != nil {
		t.Fatalf("MarshalJSON: %v", marshalErr)
	}

	const expectedFirstInstant = `"2026-04-17T15:40:00.000Z"`
	if string(got) != expectedFirstInstant {
		t.Fatalf("got %s, want %s", got, expectedFirstInstant)
	}
}

func TestIsoValueOfTheZeroTimeIsNotSpecialCased(t *testing.T) {
	got, err := isoValue(time.Time{}).MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	want := `"0001-01-01T00:00:00.000Z"`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
