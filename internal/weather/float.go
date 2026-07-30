package weather

import (
	"errors"
	"fmt"
	"math"
)

// ErrNonFinite is returned for NaN and for either infinity.
var ErrNonFinite = errors.New("value is not finite")

// scaleFloat converts a physical value to its on-chain integer representation.
//
// THE ROUNDING RULE, chosen deliberately and documented here because it is
// observable in the bytes:
//
//	Scaled floats are rounded HALF AWAY FROM ZERO, using Go's math.Round.
//	-1.5e-6 therefore encodes as -2, not -1.
//
// This is Go's native behavior, it is symmetric about zero, and it is
// deliberately NOT JavaScript's Math.round, which rounds half toward +Infinity
// and would give -1. The old TypeScript encoder used Math.round; nothing outside
// this backend decodes these values, so no consumer can observe the difference,
// and the golden file pins the rule against accidental change.
//
// Measured divergences from ECMAScript, all four asserted in float_test.go:
//
//	value       Go (this rule)   JavaScript Math.round
//	-1.2345675      -1234568              -1234567
//	-0.0000005            -1                     0
//	-1.5e-6               -2                    -1
//	-2.5e-6               -3                    -2
//
// The non-finite guard is load-bearing and is not a style choice: in Go,
// int64(math.NaN()) is implementation-defined, so without it one junk reading
// from the weather API would commit arbitrary bytes to mainnet with no error
// raised anywhere. The range check likewise runs BEFORE the conversion, so an
// overflowing float can never reach int64.
func scaleFloat(v float64) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%w: %v", ErrNonFinite, v)
	}

	scaled := math.Round(v * FloatScale)
	if scaled > float64(MaxScriptInt) || scaled < -float64(MaxScriptInt) {
		return 0, fmt.Errorf("%w: %v scaled to %v", ErrNumberOutOfRange, v, scaled)
	}

	return int64(scaled), nil
}

// unscaleFloat is the inverse of scaleFloat, to within FloatEpsilon.
//
// It is lossy by construction: the wire carries round(v*FloatScale), so any
// precision finer than 1e-6 was discarded at encode time and cannot come back.
func unscaleFloat(n int64) float64 {
	return float64(n) / FloatScale
}
