package weather

// Version is the record schema version.
//
// WARNING: the version is emitted as a SINGLE OPCODE (OP_1 = 0x51), not a data
// push. At version 17 and above it stops being a one-byte opcode and becomes a
// data push (17 encodes as 0111), which silently changes the record prefix from
// 3 bytes to 4 and the chunk count from 36 to 37. Any bump is a coordinated
// change to encoder.go, decoder.go, ChunksPerRecord and the golden file, in one
// commit.
const Version = 1

const (
	// FloatScale is the fixed-point scale for the two float fields: 6 decimals.
	FloatScale = 1_000_000

	// FloatEpsilon is the comparison tolerance for a decoded float. Encoding is
	// lossy by design: a value is stored as round(v * FloatScale).
	FloatEpsilon = 1e-6

	// DataFieldsPerRecord is the number of schema fields in every record.
	DataFieldsPerRecord = 33

	// ChunksPerRecord is the chunk count of a well-formed record script:
	// OP_FALSE, OP_RETURN, the version opcode, then 33 field pushes.
	//
	// It is 36 and not 34 because script.DecodeOptionsParseOpReturn steps PAST
	// the 0x6a byte but still appends the OP_RETURN chunk. A guard written on a
	// 34 basis accepts a script truncated by two whole fields.
	ChunksPerRecord = 36
)

// MaxScriptInt is the largest magnitude this package will encode: 2^53 - 1.
//
// The bound is not a script limitation - script numbers are arbitrary width
// after Genesis. It is the range in which a value survives a round trip through
// JSON, whose numbers are IEEE-754 doubles, and every weather value arrives as
// JSON from the Tempest API and leaves as JSON to the browser.
const MaxScriptInt = int64(1)<<53 - 1

// FieldType is the wire type of a schema field.
type FieldType uint8

const (
	// FieldInteger is emitted with appendScriptNum.
	FieldInteger FieldType = iota
	// FieldFloat is scaled by FloatScale, rounded half away from zero, then
	// emitted with appendScriptNum.
	FieldFloat
	// FieldString is emitted as a UTF-8 data push.
	FieldString
	// FieldBoolean is emitted as appendScriptNum(0) or appendScriptNum(1).
	FieldBoolean
)

// String implements fmt.Stringer.
func (t FieldType) String() string {
	switch t {
	case FieldInteger:
		return "integer"
	case FieldFloat:
		return "float"
	case FieldString:
		return "string"
	case FieldBoolean:
		return "boolean"
	default:
		return "unknown"
	}
}

// FieldDefinition is one entry of the wire schema.
//
// There is deliberately no Required flag: the wire format has no optionality.
// All 33 fields are always emitted, in FieldSchema order, and a record that is
// missing one is not a shorter record - it is a malformed script.
type FieldDefinition struct {
	Name string
	Type FieldType
}

// WeatherData is one weather reading.
//
// The JSON tags are the Tempest field names and are also the keys of the golden
// file, so they must never be renamed. The declaration order is the same strict
// alphabetical order as FieldSchema, which is the wire order.
//
// Every field is a comparable scalar, so two records can be compared with ==.
type WeatherData struct {
	AirDensity                      float64 `json:"air_density"`
	AirTemperature                  int64   `json:"air_temperature"`
	Brightness                      int64   `json:"brightness"`
	Conditions                      string  `json:"conditions"`
	DeltaT                          int64   `json:"delta_t"`
	DewPoint                        int64   `json:"dew_point"`
	FeelsLike                       int64   `json:"feels_like"`
	Icon                            string  `json:"icon"`
	IsPrecipLocalDayRainCheck       bool    `json:"is_precip_local_day_rain_check"`
	IsPrecipLocalYesterdayRainCheck bool    `json:"is_precip_local_yesterday_rain_check"`
	LightningStrikeCountLast1hr     int64   `json:"lightning_strike_count_last_1hr"`
	LightningStrikeCountLast3hr     int64   `json:"lightning_strike_count_last_3hr"`
	LightningStrikeLastDistance     int64   `json:"lightning_strike_last_distance"`
	LightningStrikeLastDistanceMsg  string  `json:"lightning_strike_last_distance_msg"`
	LightningStrikeLastEpoch        int64   `json:"lightning_strike_last_epoch"`
	PrecipAccumLocalDay             int64   `json:"precip_accum_local_day"`
	PrecipAccumLocalYesterday       int64   `json:"precip_accum_local_yesterday"`
	PrecipMinutesLocalDay           int64   `json:"precip_minutes_local_day"`
	PrecipMinutesLocalYesterday     int64   `json:"precip_minutes_local_yesterday"`
	PrecipProbability               int64   `json:"precip_probability"`
	PressureTrend                   string  `json:"pressure_trend"`
	RelativeHumidity                int64   `json:"relative_humidity"`
	SeaLevelPressure                int64   `json:"sea_level_pressure"`
	SolarRadiation                  int64   `json:"solar_radiation"`
	StationPressure                 float64 `json:"station_pressure"`
	Time                            int64   `json:"time"`
	UV                              int64   `json:"uv"`
	WetBulbGlobeTemperature         int64   `json:"wet_bulb_globe_temperature"`
	WetBulbTemperature              int64   `json:"wet_bulb_temperature"`
	WindAvg                         int64   `json:"wind_avg"`
	WindDirection                   int64   `json:"wind_direction"`
	WindDirectionCardinal           string  `json:"wind_direction_cardinal"`
	WindGust                        int64   `json:"wind_gust"`
}
