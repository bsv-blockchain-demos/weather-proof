// Package api is the HTTP read layer for the weather-proof service. It
// programs exclusively against internal/store's frozen interfaces and
// internal/store/fake in its tests: this package must NEVER import
// internal/store/postgres, which is what keeps its whole test suite
// database-free. No field in a spec §13.1 crash-list DTO may carry an
// `omitempty` json tag — an unknown value must be present and explicitly
// null, never an omitted key.
package api

import "time"

// isoMillisLayout is the ONLY timestamp shape this API emits: UTC, exactly
// three fractional digits, literal Z. Go's default RFC3339Nano marshal is
// variable-width — it trims trailing zeros — and StationRecords.tsx sorts
// timestamps with localeCompare, so a variable-width shape sorts wrong. A
// space-separated form yields Invalid Date in Safari.
const isoMillisLayout = "2006-01-02T15:04:05.000Z"

// isoMillis wraps a time.Time so it marshals in isoMillisLayout. It coerces to
// UTC on marshal: a non-UTC value would emit an offset and break the
// lexicographic sort even at the right width.
type isoMillis time.Time

// MarshalJSON emits a quoted isoMillisLayout string. It never returns an
// error: AppendFormat over a fixed layout with no error-producing input
// cannot fail, so callers need no error branch.
func (t isoMillis) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, len(isoMillisLayout)+2)
	buf = append(buf, '"')
	buf = time.Time(t).UTC().AppendFormat(buf, isoMillisLayout)
	buf = append(buf, '"')
	return buf, nil
}

// isoPtr projects a *time.Time to a *isoMillis WITHOUT dereferencing a nil.
// It is the whole nullable-pointer rule in one function: a nil in, a nil out,
// and json.Marshal writes literal null. The alternative — dereferencing and
// letting a zero time.Time through — marshals to
// "0001-01-01T00:00:00.000Z", which the frontend's formatDateTime renders as
// 01/01/0001 rather than the em dash it intends for an absent value.
func isoPtr(t *time.Time) *isoMillis {
	if t == nil {
		return nil
	}
	v := isoMillis(*t)
	return &v
}

// isoValue projects a non-nullable time.Time. Separate from isoPtr so a
// non-nullable column can never accidentally be given the nullable treatment
// and start emitting null.
func isoValue(t time.Time) isoMillis {
	return isoMillis(t)
}
