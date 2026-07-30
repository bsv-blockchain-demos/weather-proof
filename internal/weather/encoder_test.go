package weather

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
)

// TestEncodeShape checks the three fixed leading bytes and the chunk count.
func TestEncodeShape(t *testing.T) {
	for _, tc := range goldenCases() {
		data := tc.Data

		s, err := Encode(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		b := s.Bytes()
		if len(b) < 3 || b[0] != script.OpFALSE || b[1] != script.OpRETURN || b[2] != script.Op1 {
			t.Fatalf("Encode(%s) does not start 00 6a 51: %s", tc.Name, s.String())
		}

		if !s.IsData() {
			t.Errorf("Encode(%s) is not recognized as a data script", tc.Name)
		}

		chunks, err := script.DecodeScript(b, script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Fatalf("DecodeScript(%s): %v", tc.Name, err)
		}

		if len(chunks) != ChunksPerRecord {
			t.Errorf("Encode(%s) produced %d chunks, want %d", tc.Name, len(chunks), ChunksPerRecord)
		}
	}
}

// TestEncodeKnownSizes pins the measured sizes. They are the basis of the fuel
// arithmetic, so a change here changes what the app costs to run.
func TestEncodeKnownSizes(t *testing.T) {
	want := map[string]int{
		"minimal":  36,
		"sample":   99,
		"extreme":  211,
		"negative": 64,
		"oncap":    MaxScriptBytes,
	}

	for _, tc := range goldenCases() {
		data := tc.Data

		s, err := Encode(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		if got := len(*s); got != want[tc.Name] {
			t.Errorf("Encode(%s) is %d bytes, want %d", tc.Name, got, want[tc.Name])
		}
	}
}

// TestEncodeMinimalIsAllZeroBytes states the floor explicitly: 00 6a 51 followed
// by 33 OP_0 bytes.
func TestEncodeMinimalIsAllZeroBytes(t *testing.T) {
	s, err := EncodeHex(&WeatherData{})
	if err != nil {
		t.Fatalf("EncodeHex: %v", err)
	}

	want := "006a51" + strings.Repeat("00", DataFieldsPerRecord)
	if s != want {
		t.Errorf("EncodeHex(zero record) = %s, want %s", s, want)
	}
}

// TestEncodeIsDeterministic encodes the same record 100 times and requires
// byte-identical output every time. Combined with TestEncodeFollowsSchemaOrder
// this is what rules out a map ever entering the encode path.
func TestEncodeIsDeterministic(t *testing.T) {
	for _, tc := range goldenCases() {
		data := tc.Data

		first, err := EncodeHex(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		for i := range 100 {
			again, err := EncodeHex(&data)
			if err != nil {
				t.Fatalf("Encode(%s) attempt %d: %v", tc.Name, i, err)
			}

			if again != first {
				t.Fatalf("Encode(%s) is not deterministic: attempt %d differs\nfirst %s\nthen  %s",
					tc.Name, i, first, again)
			}
		}
	}
}

// TestEncodeFollowsSchemaOrder proves the emitted order is FieldSchema's slice
// order, by giving every field a distinct value and checking each chunk in place.
// A map in the encode path would fail this test on most runs.
func TestEncodeFollowsSchemaOrder(t *testing.T) {
	d := &WeatherData{}
	ptrs := d.fieldPtrs()

	// Field i gets the value i+17: above the OP_1..OP_16 opcode range, so every
	// field is a distinct data push and its position is unambiguous.
	for i, f := range FieldSchema {
		switch f.Type {
		case FieldInteger:
			p, ok := ptrs[i].(*int64)
			if !ok {
				t.Fatalf("fieldPtrs[%d] is %T, want *int64", i, ptrs[i])
			}

			*p = int64(i) + 17
		case FieldFloat:
			p, ok := ptrs[i].(*float64)
			if !ok {
				t.Fatalf("fieldPtrs[%d] is %T, want *float64", i, ptrs[i])
			}

			*p = float64(i+17) / FloatScale
		case FieldString:
			p, ok := ptrs[i].(*string)
			if !ok {
				t.Fatalf("fieldPtrs[%d] is %T, want *string", i, ptrs[i])
			}

			*p = f.Name
		case FieldBoolean:
			p, ok := ptrs[i].(*bool)
			if !ok {
				t.Fatalf("fieldPtrs[%d] is %T, want *bool", i, ptrs[i])
			}

			*p = true
		default:
			t.Fatalf("field %d has unknown type %d", i, f.Type)
		}
	}

	s, err := Encode(d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	chunks, err := script.DecodeScript(s.Bytes(), script.DecodeOptionsParseOpReturn)
	if err != nil {
		t.Fatalf("DecodeScript: %v", err)
	}

	if len(chunks) != ChunksPerRecord {
		t.Fatalf("%d chunks, want %d", len(chunks), ChunksPerRecord)
	}

	for i, f := range FieldSchema {
		c := chunks[3+i]

		switch f.Type {
		case FieldInteger, FieldFloat:
			n, numErr := scriptNumFromChunk(c)
			if numErr != nil {
				t.Errorf("chunk %d (%s): %v", i, f.Name, numErr)

				continue
			}

			if n != int64(i)+17 {
				t.Errorf("chunk %d holds %d, want %d: the emitted order is not FieldSchema's order",
					i, n, int64(i)+17)
			}
		case FieldString:
			if string(c.Data) != f.Name {
				t.Errorf("chunk %d holds %q, want %q: the emitted order is not FieldSchema's order",
					i, string(c.Data), f.Name)
			}
		case FieldBoolean:
			if c.Op != script.Op1 {
				t.Errorf("chunk %d is %#02x, want OP_1", i, c.Op)
			}
		default:
			t.Errorf("field %d has unknown type %d", i, f.Type)
		}
	}
}

// TestEncodeNeverEmitsOpReturnInAFieldPosition is the invariant that keeps the
// payload parseable: every field byte is either a push opcode (at most 0x4b),
// OP_PUSHDATA1/2 (0x4c/0x4d), OP_1NEGATE (0x4f) or OP_1..OP_16 (0x51..0x60), all
// of which are below OP_RETURN (0x6a).
func TestEncodeNeverEmitsOpReturnInAFieldPosition(t *testing.T) {
	for _, tc := range goldenCases() {
		data := tc.Data

		s, err := Encode(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		chunks, err := script.DecodeScript(s.Bytes(), script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Fatalf("DecodeScript(%s): %v", tc.Name, err)
		}

		for i := 3; i < len(chunks); i++ {
			if chunks[i].Op == script.OpRETURN {
				t.Errorf("Encode(%s): chunk %d is OP_RETURN", tc.Name, i)
			}

			if chunks[i].Op > script.Op16 {
				t.Errorf("Encode(%s): chunk %d opcode %#02x is above OP_16", tc.Name, i, chunks[i].Op)
			}
		}
	}
}

// TestEncodeCapBoundary is the money test. 297 bytes is accepted, 298 is
// rejected, and the rejection is the typed ErrScriptTooLarge that the publisher
// keys on to isolate the row before any wallet call happens.
func TestEncodeCapBoundary(t *testing.T) {
	if MaxScriptBytes != 297 {
		t.Fatalf("MaxScriptBytes = %d, want 297: it is the top of the contiguous one-claim window at D=50",
			MaxScriptBytes)
	}

	onCap := onCapRecord()

	s, err := Encode(&onCap)
	if err != nil {
		t.Fatalf("Encode(on-cap record) returned %v, want nil", err)
	}

	if len(*s) != MaxScriptBytes {
		t.Fatalf("the on-cap record is %d bytes, want exactly %d", len(*s), MaxScriptBytes)
	}

	// One more byte in a string field, so one more byte of script.
	overCap := onCapRecord()
	overCap.Icon += "B"

	_, err = Encode(&overCap)
	if !errors.Is(err, ErrScriptTooLarge) {
		t.Fatalf("Encode(298-byte record) error = %v, want ErrScriptTooLarge", err)
	}

	if !strings.Contains(err.Error(), "298") {
		t.Errorf("error %q should report the actual size so the operator can see how far over it went", err)
	}
}

// TestEncodeExtremeFixtureIsUnderTheCap records the headroom the cap was chosen
// for: the repository's own adversarial fixture is well inside it.
func TestEncodeExtremeFixtureIsUnderTheCap(t *testing.T) {
	var extreme WeatherData

	for _, tc := range goldenCases() {
		if tc.Name == "extreme" {
			extreme = tc.Data
		}
	}

	s, err := Encode(&extreme)
	if err != nil {
		t.Fatalf("Encode(extreme): %v", err)
	}

	if len(*s) > MaxScriptBytes {
		t.Fatalf("the extreme fixture is %d bytes, over the %d cap", len(*s), MaxScriptBytes)
	}

	t.Logf("extreme fixture: %d bytes, %d bytes of headroom", len(*s), MaxScriptBytes-len(*s))
}

func TestEncodeRejectsNonFiniteFloats(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		d := WeatherData{AirDensity: v}
		if _, err := Encode(&d); !errors.Is(err, ErrNonFinite) {
			t.Errorf("Encode(air_density=%v) error = %v, want ErrNonFinite", v, err)
		}

		d2 := WeatherData{StationPressure: v}
		if _, err := Encode(&d2); !errors.Is(err, ErrNonFinite) {
			t.Errorf("Encode(station_pressure=%v) error = %v, want ErrNonFinite", v, err)
		}
	}
}

func TestEncodeRejectsOutOfRangeIntegers(t *testing.T) {
	for _, n := range []int64{MaxScriptInt + 1, -MaxScriptInt - 1, math.MaxInt64, math.MinInt64} {
		d := WeatherData{Time: n}
		if _, err := Encode(&d); !errors.Is(err, ErrNumberOutOfRange) {
			t.Errorf("Encode(time=%d) error = %v, want ErrNumberOutOfRange", n, err)
		}
	}
}

// TestEncodeErrorNamesTheField checks the error text carries the field name, so
// a rejected reading is diagnosable from one log line.
func TestEncodeErrorNamesTheField(t *testing.T) {
	d := WeatherData{StationPressure: math.NaN()}

	_, err := Encode(&d)
	if err == nil {
		t.Fatal("Encode returned nil error for a NaN station_pressure")
	}

	if !strings.Contains(err.Error(), "station_pressure") {
		t.Errorf("error %q does not name the offending field", err)
	}
}

// TestEncodeRejectsAnOversizedString covers ErrStringTooLong. The cap makes this
// unreachable through Encode in practice, which is why the guard is tested at
// appendField's own level of detail: a 65536-byte string is rejected as a push,
// not merely as an oversized script.
func TestEncodeRejectsAnOversizedString(t *testing.T) {
	s := &script.Script{}
	f := FieldDefinition{Name: "conditions", Type: FieldString}
	huge := strings.Repeat("x", maxPushDataLen+1)

	err := appendField(s, f, &huge)
	if !errors.Is(err, ErrStringTooLong) {
		t.Errorf("appendField(65536-byte string) error = %v, want ErrStringTooLong", err)
	}
}

// TestEncodeRejectsANilRecord covers the other half of the nil-input hazard:
// Encode used to panic on a nil *WeatherData (d.fieldPtrs() dereferences d), and
// this pins the typed error instead.
func TestEncodeRejectsANilRecord(t *testing.T) {
	_, err := Encode(nil)
	if !errors.Is(err, ErrNilRecord) {
		t.Errorf("Encode(nil) error = %v, want ErrNilRecord", err)
	}
}

// TestEncodeStringsAreAlwaysDataPushes is why appendField calls AppendPushData
// and never appendScriptNum for strings: a one-byte string whose byte is
// 0x01..0x10 must stay recoverable, and OP_1..OP_16 carry no data.
func TestEncodeStringsAreAlwaysDataPushes(t *testing.T) {
	d := WeatherData{Conditions: "\x01", Icon: "\x10"}

	s, err := Encode(&d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	chunks, err := script.DecodeScript(s.Bytes(), script.DecodeOptionsParseOpReturn)
	if err != nil {
		t.Fatalf("DecodeScript: %v", err)
	}

	// conditions is FieldSchema index 3, icon is index 7.
	if got := string(chunks[3+3].Data); got != "\x01" {
		t.Errorf("conditions chunk holds %q, want \\x01", got)
	}

	if got := string(chunks[3+7].Data); got != "\x10" {
		t.Errorf("icon chunk holds %q, want \\x10", got)
	}
}
