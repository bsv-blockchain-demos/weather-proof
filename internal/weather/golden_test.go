package weather

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
)

// updateGolden rewrites goldenPath instead of comparing against it. Run:
//
//	go test ./internal/weather -run TestGolden -update
//
// Plain `go test` VERIFIES. CI additionally runs
// `git diff --exit-code internal/weather/testdata/golden/`, so a drifting
// encoder cannot be laundered by regenerating the file.
var updateGolden = flag.Bool("update", false, "rewrite testdata/golden/records.json from the current encoder")

// goldenPath is a string literal, not a value built at run time. That is
// deliberate: os.ReadFile on a computed path trips gosec G304, and the fix for
// that is an explained //nolint, which nolintlint then polices. A constant path
// has neither problem.
const goldenPath = "testdata/golden/records.json"

// goldenFile is the on-disk shape.
//
// FormatVersion and SchemaFieldCount are the ONLY provenance recorded. There is
// deliberately no generatedAt and no git revision: a timestamp differs between
// two runs seconds apart, and a git revision is stable within a run but changes
// on the very next commit, so either one makes regenerate-and-diff fail forever.
// A golden file must be a pure function of the code under test.
type goldenFile struct {
	FormatVersion    int            `json:"formatVersion"`
	SchemaFieldCount int            `json:"schemaFieldCount"`
	Records          []goldenRecord `json:"records"`
}

type goldenRecord struct {
	Name      string      `json:"name"`
	ScriptLen int         `json:"scriptLen"`
	ScriptHex string      `json:"scriptHex"`
	Data      WeatherData `json:"data"`
}

// goldenCase is one input record. decoder_test.go consumes goldenCases() too.
type goldenCase struct {
	Name string
	Data WeatherData
}

// goldenCases returns the frozen input set, in a fixed order.
//
//   - minimal  the all-zero floor: every field encodes as a single 0x00 byte
//   - sample   the repository's real Tempest reading
//   - extreme  the repository's adversarial fixture: unicode, punctuation, int32
//     maxima
//   - negative every numeric field negative, exercising OP_1NEGATE and the
//     sign-extension byte
//   - oncap    sits exactly on MaxScriptBytes, so a future field addition that
//     pushes the worst case over the line fails here
func goldenCases() []goldenCase {
	return []goldenCase{
		{Name: "minimal", Data: WeatherData{}},
		{Name: "sample", Data: WeatherData{
			AirDensity:                      1.29,
			AirTemperature:                  -9,
			Brightness:                      68055,
			Conditions:                      "Clear",
			DeltaT:                          2,
			DewPoint:                        -17,
			FeelsLike:                       -13,
			Icon:                            "clear-day",
			IsPrecipLocalDayRainCheck:       true,
			IsPrecipLocalYesterdayRainCheck: true,
			LightningStrikeLastDistance:     32,
			LightningStrikeLastDistanceMsg:  "30 - 34 km",
			LightningStrikeLastEpoch:        1761103981,
			PressureTrend:                   "falling",
			RelativeHumidity:                49,
			SeaLevelPressure:                1019,
			SolarRadiation:                  567,
			StationPressure:                 979.7,
			Time:                            1769529302,
			UV:                              2,
			WetBulbGlobeTemperature:         -9,
			WetBulbTemperature:              -11,
			WindAvg:                         2,
			WindDirection:                   280,
			WindDirectionCardinal:           "W",
			WindGust:                        4,
		}},
		{Name: "extreme", Data: WeatherData{
			AirDensity:                     999.999999,
			AirTemperature:                 -100,
			Brightness:                     999999,
			Conditions:                     "Extreme conditions with special chars: !@#$%^&*()",
			DeltaT:                         50,
			DewPoint:                       -50,
			FeelsLike:                      -120,
			Icon:                           "extreme-weather-⚡️",
			IsPrecipLocalDayRainCheck:      true,
			LightningStrikeCountLast1hr:    999,
			LightningStrikeCountLast3hr:    9999,
			LightningStrikeLastDistance:    999,
			LightningStrikeLastDistanceMsg: "Very far away with unicode: ⚡⚡",
			LightningStrikeLastEpoch:       2147483647,
			PrecipAccumLocalDay:            999,
			PrecipAccumLocalYesterday:      999,
			PrecipMinutesLocalDay:          1440,
			PrecipMinutesLocalYesterday:    1440,
			PrecipProbability:              100,
			PressureTrend:                  "rapidly falling",
			RelativeHumidity:               100,
			SeaLevelPressure:               2000,
			SolarRadiation:                 9999,
			StationPressure:                1234.56789,
			Time:                           2147483647,
			UV:                             20,
			WetBulbGlobeTemperature:        60,
			WetBulbTemperature:             50,
			WindAvg:                        200,
			WindDirection:                  359,
			WindDirectionCardinal:          "NNE",
			WindGust:                       300,
		}},
		{Name: "negative", Data: WeatherData{
			AirDensity:                     -1.234567,
			AirTemperature:                 -1,
			Brightness:                     -68055,
			Conditions:                     "neg",
			DeltaT:                         -1,
			DewPoint:                       -128,
			FeelsLike:                      -256,
			Icon:                           "n",
			LightningStrikeCountLast1hr:    -1,
			LightningStrikeCountLast3hr:    -16,
			LightningStrikeLastDistance:    -17,
			LightningStrikeLastEpoch:       -2147483648,
			PrecipAccumLocalDay:            -1,
			PrecipAccumLocalYesterday:      -1,
			PrecipMinutesLocalDay:          -1,
			PrecipMinutesLocalYesterday:    -1,
			PrecipProbability:              -1,
			RelativeHumidity:               -1,
			SeaLevelPressure:               -1,
			SolarRadiation:                 -1,
			StationPressure:                -0.000001,
			Time:                           -MaxScriptInt,
			UV:                             -1,
			WetBulbGlobeTemperature:        -1,
			WetBulbTemperature:             -1,
			WindAvg:                        -1,
			WindDirection:                  -1,
			LightningStrikeLastDistanceMsg: "",
			WindGust:                       -1,
		}},
		{Name: "oncap", Data: onCapRecord()},
	}
}

// onCapRecord returns a record that encodes to EXACTLY MaxScriptBytes.
//
// The arithmetic, so it can be re-derived rather than trusted: the all-zero
// floor is 36 bytes (3 prefix + 33 single-byte fields), leaving 261 bytes of
// budget. A 200-byte string costs OP_PUSHDATA1 + a length byte + 200 = 202 in
// place of 1, so +201. A 60-byte string costs a length opcode + 60 = 61 in place
// of 1, so +60. 36 + 201 + 60 = 297.
func onCapRecord() WeatherData {
	return WeatherData{
		Conditions: strings.Repeat("A", 200),
		Icon:       strings.Repeat("B", 60),
	}
}

func TestGolden(t *testing.T) {
	cases := goldenCases()

	want := goldenFile{
		FormatVersion:    Version,
		SchemaFieldCount: DataFieldsPerRecord,
		Records:          make([]goldenRecord, 0, len(cases)),
	}

	for _, tc := range cases {
		data := tc.Data

		s, err := Encode(&data)
		if err != nil {
			t.Fatalf("Encode(%s): %v", tc.Name, err)
		}

		want.Records = append(want.Records, goldenRecord{
			Name:      tc.Name,
			ScriptLen: len(*s),
			ScriptHex: s.String(),
			Data:      tc.Data,
		})
	}

	produced, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	produced = append(produced, '\n')

	if *updateGolden {
		if mkErr := os.MkdirAll("testdata/golden", 0o750); mkErr != nil {
			t.Fatalf("mkdir: %v", mkErr)
		}

		if writeErr := os.WriteFile(goldenPath, produced, 0o600); writeErr != nil {
			t.Fatalf("write: %v", writeErr)
		}

		t.Logf("wrote %s (%d records, %d bytes)", goldenPath, len(want.Records), len(produced))

		return
	}

	onDisk, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./internal/weather -run TestGolden -update)", goldenPath, err)
	}

	if !bytes.Equal(produced, onDisk) {
		t.Errorf("%s is out of date.\nThe encoder now produces different bytes for at least one golden record.\n"+
			"If that change is intended, review it and run:\n"+
			"  go test ./internal/weather -run TestGolden -update\n"+
			"produced %d bytes, on disk %d bytes", goldenPath, len(produced), len(onDisk))
	}
}

// TestGoldenFileHasNoTimestampOrRevision guards the reproducibility trap
// directly: if anyone adds a generatedAt or a git revision to the file, every
// later commit fails the CI diff.
func TestGoldenFileHasNoTimestampOrRevision(t *testing.T) {
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal %s: %v", goldenPath, err)
	}

	allowed := map[string]bool{"formatVersion": true, "schemaFieldCount": true, "records": true}
	for key := range parsed {
		if !allowed[key] {
			t.Errorf("unexpected top-level key %q in %s: the file must be a pure function of the code, "+
				"so no timestamp and no git revision", key, goldenPath)
		}
	}

	for _, banned := range []string{"generatedAt", "generated_at", "gitRev", "commit", "timestamp"} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Errorf("%s contains %q, which makes regenerate-and-diff fail on the next commit", goldenPath, banned)
		}
	}
}
