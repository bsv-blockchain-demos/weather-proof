package weather

// FieldSchema IS THE WIRE FORMAT.
//
// The order below is the on-chain field layout and must never change: strict
// alphabetical order, air_density first and wind_gust last. It is ported from
// the TypeScript src/format/schema.ts, which is the only authority for it.
//
// Do NOT port the order from ENCODING.md. That document's "Field Order" section
// lists a time-first, category-grouped order which was superseded on chain; it
// is kept as history, not as a specification.
//
// It is a SLICE, never a map. Ranging over a map would make the encoder
// non-deterministic, which is the single easiest way to corrupt this format.
var FieldSchema = []FieldDefinition{
	{Name: "air_density", Type: FieldFloat},                            //  0
	{Name: "air_temperature", Type: FieldInteger},                      //  1
	{Name: "brightness", Type: FieldInteger},                           //  2
	{Name: "conditions", Type: FieldString},                            //  3
	{Name: "delta_t", Type: FieldInteger},                              //  4
	{Name: "dew_point", Type: FieldInteger},                            //  5
	{Name: "feels_like", Type: FieldInteger},                           //  6
	{Name: "icon", Type: FieldString},                                  //  7
	{Name: "is_precip_local_day_rain_check", Type: FieldBoolean},       //  8
	{Name: "is_precip_local_yesterday_rain_check", Type: FieldBoolean}, //  9
	{Name: "lightning_strike_count_last_1hr", Type: FieldInteger},      // 10
	{Name: "lightning_strike_count_last_3hr", Type: FieldInteger},      // 11
	{Name: "lightning_strike_last_distance", Type: FieldInteger},       // 12
	{Name: "lightning_strike_last_distance_msg", Type: FieldString},    // 13
	{Name: "lightning_strike_last_epoch", Type: FieldInteger},          // 14
	{Name: "precip_accum_local_day", Type: FieldInteger},               // 15
	{Name: "precip_accum_local_yesterday", Type: FieldInteger},         // 16
	{Name: "precip_minutes_local_day", Type: FieldInteger},             // 17
	{Name: "precip_minutes_local_yesterday", Type: FieldInteger},       // 18
	{Name: "precip_probability", Type: FieldInteger},                   // 19
	{Name: "pressure_trend", Type: FieldString},                        // 20
	{Name: "relative_humidity", Type: FieldInteger},                    // 21
	{Name: "sea_level_pressure", Type: FieldInteger},                   // 22
	{Name: "solar_radiation", Type: FieldInteger},                      // 23
	{Name: "station_pressure", Type: FieldFloat},                       // 24
	{Name: "time", Type: FieldInteger},                                 // 25
	{Name: "uv", Type: FieldInteger},                                   // 26
	{Name: "wet_bulb_globe_temperature", Type: FieldInteger},           // 27
	{Name: "wet_bulb_temperature", Type: FieldInteger},                 // 28
	{Name: "wind_avg", Type: FieldInteger},                             // 29
	{Name: "wind_direction", Type: FieldInteger},                       // 30
	{Name: "wind_direction_cardinal", Type: FieldString},               // 31
	{Name: "wind_gust", Type: FieldInteger},                            // 32
}

// fieldPtrs returns pointers to the 33 wire fields of d in FieldSchema order.
//
// Index i of the result corresponds to index i of FieldSchema, and the concrete
// pointer type matches FieldSchema[i].Type:
//
//	FieldInteger -> *int64    FieldFloat   -> *float64
//	FieldString  -> *string   FieldBoolean -> *bool
//
// One list serves both the encoder (which reads through the pointers) and the
// decoder (which writes through them), so the two can never drift apart.
func (d *WeatherData) fieldPtrs() []any {
	return []any{
		&d.AirDensity,                      //  0 air_density                          float
		&d.AirTemperature,                  //  1 air_temperature                      integer
		&d.Brightness,                      //  2 brightness                           integer
		&d.Conditions,                      //  3 conditions                           string
		&d.DeltaT,                          //  4 delta_t                              integer
		&d.DewPoint,                        //  5 dew_point                            integer
		&d.FeelsLike,                       //  6 feels_like                           integer
		&d.Icon,                            //  7 icon                                 string
		&d.IsPrecipLocalDayRainCheck,       //  8 is_precip_local_day_rain_check       boolean
		&d.IsPrecipLocalYesterdayRainCheck, //  9 is_precip_local_yesterday_rain_check boolean
		&d.LightningStrikeCountLast1hr,     // 10 lightning_strike_count_last_1hr      integer
		&d.LightningStrikeCountLast3hr,     // 11 lightning_strike_count_last_3hr      integer
		&d.LightningStrikeLastDistance,     // 12 lightning_strike_last_distance       integer
		&d.LightningStrikeLastDistanceMsg,  // 13 lightning_strike_last_distance_msg   string
		&d.LightningStrikeLastEpoch,        // 14 lightning_strike_last_epoch          integer
		&d.PrecipAccumLocalDay,             // 15 precip_accum_local_day               integer
		&d.PrecipAccumLocalYesterday,       // 16 precip_accum_local_yesterday         integer
		&d.PrecipMinutesLocalDay,           // 17 precip_minutes_local_day             integer
		&d.PrecipMinutesLocalYesterday,     // 18 precip_minutes_local_yesterday       integer
		&d.PrecipProbability,               // 19 precip_probability                   integer
		&d.PressureTrend,                   // 20 pressure_trend                       string
		&d.RelativeHumidity,                // 21 relative_humidity                    integer
		&d.SeaLevelPressure,                // 22 sea_level_pressure                   integer
		&d.SolarRadiation,                  // 23 solar_radiation                      integer
		&d.StationPressure,                 // 24 station_pressure                     float
		&d.Time,                            // 25 time                                 integer
		&d.UV,                              // 26 uv                                   integer
		&d.WetBulbGlobeTemperature,         // 27 wet_bulb_globe_temperature           integer
		&d.WetBulbTemperature,              // 28 wet_bulb_temperature                 integer
		&d.WindAvg,                         // 29 wind_avg                             integer
		&d.WindDirection,                   // 30 wind_direction                       integer
		&d.WindDirectionCardinal,           // 31 wind_direction_cardinal              string
		&d.WindGust,                        // 32 wind_gust                            integer
	}
}
