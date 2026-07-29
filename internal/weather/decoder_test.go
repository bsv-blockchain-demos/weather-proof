package weather

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
)

// equalRecords compares two records: the two float fields within FloatEpsilon,
// because fixed-point encoding at 1e-6 is lossy by design, and every other field
// exactly. WeatherData is all scalars, so == covers the remaining 31 fields.
func equalRecords(a, b WeatherData) bool {
	if math.Abs(a.AirDensity-b.AirDensity) > FloatEpsilon {
		return false
	}

	if math.Abs(a.StationPressure-b.StationPressure) > FloatEpsilon {
		return false
	}

	a.AirDensity, b.AirDensity = 0, 0
	a.StationPressure, b.StationPressure = 0, 0

	return a == b
}

// TestRoundTripGoldenCases is the correctness test: whatever Encode writes,
// Decode reads back. The golden file is the change detector; this is the part
// that says the format is self-consistent.
func TestRoundTripGoldenCases(t *testing.T) {
	for _, tc := range goldenCases() {
		data := tc.Data

		s, err := Encode(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		got, err := Decode(s)
		if err != nil {
			t.Fatalf("Decode(%s): %v", tc.Name, err)
		}

		if !equalRecords(tc.Data, *got) {
			t.Errorf("round trip of %s changed the record\nin  %+v\nout %+v", tc.Name, tc.Data, *got)
		}
	}
}

// TestDecodeGoldenHex decodes the hex COMMITTED in the golden file rather than
// hex produced in this process, so the stored bytes are proved readable and not
// merely self-consistent.
func TestDecodeGoldenHex(t *testing.T) {
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}

	var file goldenFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("unmarshal %s: %v", goldenPath, err)
	}

	if len(file.Records) == 0 {
		t.Fatalf("%s holds no records", goldenPath)
	}

	if file.SchemaFieldCount != DataFieldsPerRecord {
		t.Fatalf("%s says %d fields, this package has %d",
			goldenPath, file.SchemaFieldCount, DataFieldsPerRecord)
	}

	for _, rec := range file.Records {
		got, err := DecodeHex(rec.ScriptHex)
		if err != nil {
			t.Errorf("DecodeHex(%s): %v", rec.Name, err)

			continue
		}

		if !equalRecords(rec.Data, *got) {
			t.Errorf("golden %s decoded to a different record\nstored  %+v\ndecoded %+v", rec.Name, rec.Data, *got)
		}

		if len(rec.ScriptHex) != rec.ScriptLen*2 {
			t.Errorf("golden %s: scriptLen %d does not match %d hex characters",
				rec.Name, rec.ScriptLen, len(rec.ScriptHex))
		}
	}
}

// TestRoundTripEdgeCases covers the value shapes the golden set does not, one
// field at a time so a failure names the cause.
func TestRoundTripEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		data WeatherData
	}{
		{"all zero", WeatherData{}},
		{"empty strings only", WeatherData{Time: 1}},
		{"unicode strings", WeatherData{
			Conditions:                     "☔ ☂ ☀",
			Icon:                           "partly-cloudy-night-⚡️",
			PressureTrend:                  "→",
			LightningStrikeLastDistanceMsg: "ünïcødé",
			WindDirectionCardinal:          "N",
		}},
		{"one byte strings in the opcode range", WeatherData{
			Conditions:                     "\x01",
			Icon:                           "\x10",
			PressureTrend:                  "\x00",
			LightningStrikeLastDistanceMsg: "\x81",
			WindDirectionCardinal:          "\x4f",
		}},
		{"75 byte string", WeatherData{Conditions: strings.Repeat("c", 75)}},
		{"76 byte string crosses into OP_PUSHDATA1", WeatherData{Conditions: strings.Repeat("c", 76)}},
		{"both booleans true", WeatherData{
			IsPrecipLocalDayRainCheck:       true,
			IsPrecipLocalYesterdayRainCheck: true,
		}},
		{"one boolean true", WeatherData{IsPrecipLocalYesterdayRainCheck: true}},
		{"opcode range integers", WeatherData{
			AirTemperature: 1, Brightness: 16, DeltaT: 17, DewPoint: -1,
			FeelsLike: 15, RelativeHumidity: 2,
		}},
		{"sign extension integers", WeatherData{
			AirTemperature: 127, Brightness: 128, DeltaT: -127, DewPoint: -128,
			FeelsLike: 255, SeaLevelPressure: -255, SolarRadiation: 256,
		}},
		{"maximum magnitudes", WeatherData{
			Time: MaxScriptInt, Brightness: -MaxScriptInt,
			LightningStrikeLastEpoch: 2147483647, SolarRadiation: -2147483648,
		}},
		{"maximum floats", WeatherData{AirDensity: 9e9, StationPressure: -9e9}},
		{"smallest floats", WeatherData{AirDensity: 0.000001, StationPressure: -0.000001}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := tc.data

			s, err := Encode(&data)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			got, err := Decode(s)
			if err != nil {
				t.Fatalf("Decode(%s): %v", s.String(), err)
			}

			if !equalRecords(tc.data, *got) {
				t.Errorf("round trip changed the record\nin  %+v\nout %+v\nhex %s", tc.data, *got, s.String())
			}
		})
	}
}

// TestRoundTripSweep is the property test, done as a deterministic sweep rather
// than with a random generator: it is reproducible from nothing but the source,
// and it needs no pseudo-random number generator (math/rand in a test trips the
// gosec weak-randomness check, and hand-rolling one trips the integer-conversion
// check).
//
// Record k gives field i the value at index (i+k) mod len(table) of the table for
// that field's type, so across 600 records every value meets every field and a
// great many cross-field combinations are covered.
func TestRoundTripSweep(t *testing.T) {
	ints := []int64{
		0, 1, -1, 2, -2, 15, 16, 17, -16, -17, 127, -127, 128, -128, 255, -255,
		256, -256, 32767, -32768, 65535, 16777215, 2147483647, -2147483648,
		4294967296, MaxScriptInt, -MaxScriptInt,
	}
	floats := []float64{0, 0.000001, -0.000001, 1.29, -1.234567, 979.7, 999.999999, 9e9, -9e9}
	strs := []string{"", "a", "\x01", "\x10", "\x81", "abc def", "ünïcødé"}
	bools := []bool{false, true}

	for k := range 600 {
		d := WeatherData{}
		ptrs := d.fieldPtrs()

		for i, f := range FieldSchema {
			switch f.Type {
			case FieldInteger:
				p, ok := ptrs[i].(*int64)
				if !ok {
					t.Fatalf("fieldPtrs[%d] is %T, want *int64", i, ptrs[i])
				}

				*p = ints[(i+k)%len(ints)]
			case FieldFloat:
				p, ok := ptrs[i].(*float64)
				if !ok {
					t.Fatalf("fieldPtrs[%d] is %T, want *float64", i, ptrs[i])
				}

				*p = floats[(i+k)%len(floats)]
			case FieldString:
				p, ok := ptrs[i].(*string)
				if !ok {
					t.Fatalf("fieldPtrs[%d] is %T, want *string", i, ptrs[i])
				}

				*p = strs[(i+k)%len(strs)]
			case FieldBoolean:
				p, ok := ptrs[i].(*bool)
				if !ok {
					t.Fatalf("fieldPtrs[%d] is %T, want *bool", i, ptrs[i])
				}

				*p = bools[(i+k)%len(bools)]
			default:
				t.Fatalf("field %d has unknown type %d", i, f.Type)
			}
		}

		want := d

		s, err := Encode(&d)
		if err != nil {
			t.Fatalf("record %d: Encode(%+v): %v", k, want, err)
		}

		if len(*s) > MaxScriptBytes {
			t.Fatalf("record %d is %d bytes, over the %d cap: shrink the sweep tables",
				k, len(*s), MaxScriptBytes)
		}

		got, err := Decode(s)
		if err != nil {
			t.Fatalf("record %d: Decode(%s): %v", k, s.String(), err)
		}

		if !equalRecords(want, *got) {
			t.Fatalf("record %d round trip changed the record\nin  %+v\nout %+v\nhex %s",
				k, want, *got, s.String())
		}
	}
}

func TestDecodeRejectsWrongVersion(t *testing.T) {
	s, err := Encode(&WeatherData{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	for _, op := range []byte{script.Op0, script.Op2, script.Op16, script.Op1NEGATE} {
		b := append([]byte(nil), s.Bytes()...)
		b[2] = op

		_, err := Decode(script.NewFromBytes(b))
		if !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("version opcode %#02x: error = %v, want ErrUnsupportedVersion", op, err)
		}
	}
}

func TestDecodeRejectsTooFewChunks(t *testing.T) {
	s, err := Encode(&WeatherData{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Every field of the all-zero record is exactly one byte, so dropping the
	// last two bytes drops two whole fields and leaves a 34-chunk script - the
	// shape a guard written on the wrong basis would wrongly accept.
	b := s.Bytes()

	_, err = Decode(script.NewFromBytes(b[:len(b)-2]))
	if !errors.Is(err, ErrMalformedScript) {
		t.Fatalf("34-chunk script: error = %v, want ErrMalformedScript", err)
	}

	if !strings.Contains(err.Error(), "34 chunks") {
		t.Errorf("error %q should say how many chunks it found", err)
	}
}

func TestDecodeRejectsAMissingPrefix(t *testing.T) {
	// A bare version opcode plus 33 zero fields: 34 chunks, no 00 6a.
	bare := make([]byte, 0, 1+DataFieldsPerRecord)
	bare = append(bare, script.Op1)

	for range DataFieldsPerRecord {
		bare = append(bare, script.Op0)
	}

	if _, err := Decode(script.NewFromBytes(bare)); !errors.Is(err, ErrMalformedScript) {
		t.Errorf("prefix-less script: error = %v, want ErrMalformedScript", err)
	}

	// And a full-length script whose first two bytes are not 00 6a.
	padded := make([]byte, 0, 3+DataFieldsPerRecord)
	padded = append(padded, script.Op1, script.Op1, script.Op1)

	for range DataFieldsPerRecord {
		padded = append(padded, script.Op0)
	}

	if _, err := Decode(script.NewFromBytes(padded)); !errors.Is(err, ErrMalformedScript) {
		t.Errorf("wrong prefix: error = %v, want ErrMalformedScript", err)
	}
}

func TestDecodeToleratesTrailingChunks(t *testing.T) {
	in := WeatherData{Conditions: "Clear", Time: 1769529302}

	s, err := Encode(&in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	b := append([]byte(nil), s.Bytes()...)
	b = append(b, script.Op1, script.Op2, script.Op16)

	got, err := Decode(script.NewFromBytes(b))
	if err != nil {
		t.Fatalf("Decode with trailing chunks: %v", err)
	}

	if !equalRecords(in, *got) {
		t.Errorf("trailing chunks changed the record\nin  %+v\nout %+v", in, *got)
	}
}

func TestDecodeRejectsOpReturnInAFieldPosition(t *testing.T) {
	// air_density (schema index 0) is replaced by a bare OP_RETURN, and one extra
	// zero field is appended so the chunk count still reaches 36.
	b := make([]byte, 0, 4+DataFieldsPerRecord)
	b = append(b, script.OpFALSE, script.OpRETURN, script.Op1, script.OpRETURN)

	for range DataFieldsPerRecord {
		b = append(b, script.Op0)
	}

	_, err := Decode(script.NewFromBytes(b))
	if !errors.Is(err, ErrMalformedScript) {
		t.Fatalf("OP_RETURN in a numeric field: error = %v, want ErrMalformedScript", err)
	}

	if !strings.Contains(err.Error(), "air_density") {
		t.Errorf("error %q does not name the offending field", err)
	}

	// And in a string position: conditions is schema index 3.
	b2 := []byte{script.OpFALSE, script.OpRETURN, script.Op1}
	for i := range DataFieldsPerRecord {
		if i == 3 {
			b2 = append(b2, script.OpRETURN)

			continue
		}

		b2 = append(b2, script.Op0)
	}

	b2 = append(b2, script.Op0)

	_, err = Decode(script.NewFromBytes(b2))
	if !errors.Is(err, ErrMalformedScript) {
		t.Fatalf("OP_RETURN in a string field: error = %v, want ErrMalformedScript", err)
	}

	if !strings.Contains(err.Error(), "conditions") {
		t.Errorf("error %q does not name the offending field", err)
	}
}

func TestDecodeRejectsANonMinimalPush(t *testing.T) {
	// air_density carries 7f00, which decodes numerically to 127 but is not
	// minimally encoded. Accepting it would mean two distinct scripts decode to
	// the same record, so Encode would no longer be the only writer of a record's
	// bytes.
	b := make([]byte, 0, 6+DataFieldsPerRecord)
	b = append(b, script.OpFALSE, script.OpRETURN, script.Op1, script.OpDATA2, 0x7f, 0x00)

	for range DataFieldsPerRecord {
		b = append(b, script.Op0)
	}

	if _, err := Decode(script.NewFromBytes(b)); !errors.Is(err, ErrMalformedScript) {
		t.Errorf("non-minimal push: error = %v, want ErrMalformedScript", err)
	}
}

func TestDecodeRejectsAnOpcodeInAStringPosition(t *testing.T) {
	// conditions (index 3) carries OP_1, which is a valid number but carries no
	// data, so accepting it would silently decode as the empty string.
	b := []byte{script.OpFALSE, script.OpRETURN, script.Op1}
	for i := range DataFieldsPerRecord {
		if i == 3 {
			b = append(b, script.Op1)

			continue
		}

		b = append(b, script.Op0)
	}

	_, err := Decode(script.NewFromBytes(b))
	if !errors.Is(err, ErrMalformedScript) {
		t.Fatalf("OP_1 in a string field: error = %v, want ErrMalformedScript", err)
	}

	if !strings.Contains(err.Error(), "not a data push") {
		t.Errorf("error %q should say the chunk is not a data push", err)
	}
}

func TestDecodeHexRejectsBadHex(t *testing.T) {
	for _, h := range []string{"zz", "006a5", ""} {
		if _, err := DecodeHex(h); !errors.Is(err, ErrMalformedScript) {
			t.Errorf("DecodeHex(%q) error = %v, want ErrMalformedScript", h, err)
		}
	}
}

func TestIsValidScript(t *testing.T) {
	good, err := Encode(&WeatherData{Conditions: "Clear"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if !IsValidScript(good) {
		t.Error("IsValidScript returned false for a script this package just encoded")
	}

	if IsValidScript(script.NewFromBytes([]byte{script.OpFALSE, script.OpRETURN})) {
		t.Error("IsValidScript returned true for a bare OP_FALSE OP_RETURN")
	}

	if IsValidScript(script.NewFromBytes(nil)) {
		t.Error("IsValidScript returned true for an empty script")
	}
}
