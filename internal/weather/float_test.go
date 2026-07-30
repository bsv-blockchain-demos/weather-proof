package weather

import (
	"errors"
	"math"
	"testing"
)

// TestScaleFloatRoundingRule is the documented rule, asserted. Half away from
// zero, Go's math.Round. The four negative-half cases are the ones where
// JavaScript's Math.round disagrees, and the JS column is recorded so nobody
// "fixes" this back toward JavaScript by accident.
func TestScaleFloatRoundingRule(t *testing.T) {
	cases := []struct {
		in     float64
		want   int64
		wantJS int64 // what ECMAScript Math.round would have produced
	}{
		{-1.2345675, -1234568, -1234567},
		{-0.0000005, -1, 0},
		{-1.5e-6, -2, -1},
		{-2.5e-6, -3, -2},
		{1.5e-6, 2, 2},
		{2.5e-6, 3, 3},
		{0.0000005, 1, 1},
		{0, 0, 0},
		{-0, 0, 0},
		{1.29, 1290000, 1290000},
		{979.7, 979700000, 979700000},
		{999.999999, 999999999, 999999999},
		{1234.56789, 1234567890, 1234567890},
		{-1.234567, -1234567, -1234567},
		{-0.000001, -1, -1},
	}

	for _, tc := range cases {
		got, err := scaleFloat(tc.in)
		if err != nil {
			t.Errorf("scaleFloat(%v) returned %v, want nil", tc.in, err)

			continue
		}

		if got != tc.want {
			t.Errorf("scaleFloat(%v) = %d, want %d (JavaScript would give %d; the Go rule is half away from zero)",
				tc.in, got, tc.want, tc.wantJS)
		}
	}
}

func TestScaleFloatRejectsNonFinite(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := scaleFloat(v)
		if !errors.Is(err, ErrNonFinite) {
			t.Errorf("scaleFloat(%v) error = %v, want ErrNonFinite", v, err)
		}
	}
}

func TestScaleFloatRejectsOutOfRange(t *testing.T) {
	// 1e10 * 1e6 = 1e16, which is above 2^53-1 = 9.007e15.
	for _, v := range []float64{1e10, -1e10, 1e300} {
		_, err := scaleFloat(v)
		if !errors.Is(err, ErrNumberOutOfRange) {
			t.Errorf("scaleFloat(%v) error = %v, want ErrNumberOutOfRange", v, err)
		}
	}
}

// TestScaleFloatBoundary pins both sides of the MaxScriptInt edge.
//
// Note that 2^53-1 is NOT reachable through the float path: 9007199254.740991
// is not exactly representable, and scaling it back up lands on 9007199254740992
// - one above the bound - so it is correctly rejected. The largest value that
// passes is therefore stated as a round number.
func TestScaleFloatBoundary(t *testing.T) {
	got, err := scaleFloat(9e9)
	if err != nil {
		t.Fatalf("scaleFloat(9e9) returned %v, want nil", err)
	}

	if got != 9_000_000_000_000_000 {
		t.Errorf("scaleFloat(9e9) = %d, want 9000000000000000", got)
	}

	onEdge := 9_007_199_254.74

	got, err = scaleFloat(onEdge)
	if err != nil {
		t.Fatalf("scaleFloat(%v) returned %v, want nil", onEdge, err)
	}

	if got != 9_007_199_254_740_000 {
		t.Errorf("scaleFloat(%v) = %d, want 9007199254740000", onEdge, got)
	}

	overEdge := 9_007_199_254.741
	if _, err := scaleFloat(overEdge); !errors.Is(err, ErrNumberOutOfRange) {
		t.Errorf("scaleFloat(%v) error = %v, want ErrNumberOutOfRange", overEdge, err)
	}
}

func TestUnscaleFloat(t *testing.T) {
	cases := []struct {
		in   int64
		want float64
	}{
		{0, 0},
		{1, 0.000001},
		{-1, -0.000001},
		{1290000, 1.29},
		{979700000, 979.7},
		{999999999, 999.999999},
		{-1234567, -1.234567},
	}

	for _, tc := range cases {
		if got := unscaleFloat(tc.in); math.Abs(got-tc.want) > FloatEpsilon {
			t.Errorf("unscaleFloat(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestFloatRoundTripIsLossyOnlyBelowEpsilon states the precision contract: a
// value that is an exact multiple of 1e-6 survives exactly, and anything finer
// is quantized, not preserved.
func TestFloatRoundTripIsLossyOnlyBelowEpsilon(t *testing.T) {
	exact := []float64{0, 1.29, 979.7, 999.999999, -1.234567, 1234.56789}
	for _, v := range exact {
		n, err := scaleFloat(v)
		if err != nil {
			t.Fatalf("scaleFloat(%v): %v", v, err)
		}

		if got := unscaleFloat(n); got != v {
			t.Errorf("round trip of %v gave %v, want it exact", v, got)
		}
	}

	// 7 decimals: the last digit is discarded, and the result differs from the
	// input by less than FloatEpsilon.
	n, err := scaleFloat(1.2345678)
	if err != nil {
		t.Fatalf("scaleFloat(1.2345678): %v", err)
	}

	if n != 1234568 {
		t.Errorf("scaleFloat(1.2345678) = %d, want 1234568", n)
	}

	if diff := math.Abs(unscaleFloat(n) - 1.2345678); diff > FloatEpsilon {
		t.Errorf("quantization error %v exceeds FloatEpsilon", diff)
	}
}
