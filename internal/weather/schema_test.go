package weather

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// wantFieldNames is the wire order, transcribed independently from the
// TypeScript src/format/schema.ts. It is deliberately duplicated rather than
// derived from FieldSchema: a test that reads the value it is checking proves
// nothing.
var wantFieldNames = []string{
	"air_density",
	"air_temperature",
	"brightness",
	"conditions",
	"delta_t",
	"dew_point",
	"feels_like",
	"icon",
	"is_precip_local_day_rain_check",
	"is_precip_local_yesterday_rain_check",
	"lightning_strike_count_last_1hr",
	"lightning_strike_count_last_3hr",
	"lightning_strike_last_distance",
	"lightning_strike_last_distance_msg",
	"lightning_strike_last_epoch",
	"precip_accum_local_day",
	"precip_accum_local_yesterday",
	"precip_minutes_local_day",
	"precip_minutes_local_yesterday",
	"precip_probability",
	"pressure_trend",
	"relative_humidity",
	"sea_level_pressure",
	"solar_radiation",
	"station_pressure",
	"time",
	"uv",
	"wet_bulb_globe_temperature",
	"wet_bulb_temperature",
	"wind_avg",
	"wind_direction",
	"wind_direction_cardinal",
	"wind_gust",
}

func TestFieldSchemaOrderAndCount(t *testing.T) {
	if len(FieldSchema) != DataFieldsPerRecord {
		t.Fatalf("len(FieldSchema) = %d, want %d", len(FieldSchema), DataFieldsPerRecord)
	}

	if len(wantFieldNames) != DataFieldsPerRecord {
		t.Fatalf("the test's own list has %d names, want %d", len(wantFieldNames), DataFieldsPerRecord)
	}

	for i, want := range wantFieldNames {
		if FieldSchema[i].Name != want {
			t.Errorf("FieldSchema[%d].Name = %q, want %q: the order IS the wire format",
				i, FieldSchema[i].Name, want)
		}
	}
}

func TestFieldSchemaIsStrictlyAlphabetical(t *testing.T) {
	names := make([]string, 0, len(FieldSchema))
	for _, f := range FieldSchema {
		names = append(names, f.Name)
	}

	sorted := slices.Clone(names)
	slices.Sort(sorted)

	for i := range names {
		if names[i] != sorted[i] {
			t.Fatalf("FieldSchema[%d] = %q but alphabetical order wants %q: port the order from schema.ts, never from ENCODING.md",
				i, names[i], sorted[i])
		}
	}
}

func TestFieldSchemaTypeTally(t *testing.T) {
	tally := map[FieldType]int{}
	for _, f := range FieldSchema {
		tally[f.Type]++
	}

	want := map[FieldType]int{
		FieldInteger: 24,
		FieldFloat:   2,
		FieldString:  5,
		FieldBoolean: 2,
	}

	for ft, n := range want {
		if tally[ft] != n {
			t.Errorf("%s field count = %d, want %d", ft, tally[ft], n)
		}
	}
}

func TestSchemaNamesMatchJSONTags(t *testing.T) {
	tags := jsonTags(t)

	if len(tags) != len(FieldSchema) {
		t.Fatalf("%d json tags, %d schema entries", len(tags), len(FieldSchema))
	}

	for i, f := range FieldSchema {
		if tags[i] != f.Name {
			t.Errorf("index %d: json tag %q, schema name %q", i, tags[i], f.Name)
		}
	}
}

func TestFieldPtrsMatchSchemaTypes(t *testing.T) {
	d := &WeatherData{}
	ptrs := d.fieldPtrs()

	if len(ptrs) != len(FieldSchema) {
		t.Fatalf("fieldPtrs returned %d pointers for %d schema fields", len(ptrs), len(FieldSchema))
	}

	wantType := map[FieldType]string{
		FieldInteger: "*int64",
		FieldFloat:   "*float64",
		FieldString:  "*string",
		FieldBoolean: "*bool",
	}

	for i, f := range FieldSchema {
		if got := reflect.TypeOf(ptrs[i]).String(); got != wantType[f.Type] {
			t.Errorf("fieldPtrs[%d] (%s) is %s, want %s for schema type %s",
				i, f.Name, got, wantType[f.Type], f.Type)
		}
	}
}

// TestFieldPtrsAreDistinctAndInStructOrder writes a unique marker through every
// pointer and reads it back through JSON, so a copy-paste slip in fieldPtrs -
// the same struct field listed twice - cannot survive.
func TestFieldPtrsAreDistinctAndInStructOrder(t *testing.T) {
	d := &WeatherData{}
	ptrs := d.fieldPtrs()

	for i, f := range FieldSchema {
		switch f.Type {
		case FieldInteger:
			p, ok := ptrs[i].(*int64)
			if !ok {
				t.Fatalf("fieldPtrs[%d] (%s) is %T, want *int64", i, f.Name, ptrs[i])
			}

			*p = int64(i) + 1
		case FieldFloat:
			p, ok := ptrs[i].(*float64)
			if !ok {
				t.Fatalf("fieldPtrs[%d] (%s) is %T, want *float64", i, f.Name, ptrs[i])
			}

			*p = float64(i) + 1
		case FieldString:
			p, ok := ptrs[i].(*string)
			if !ok {
				t.Fatalf("fieldPtrs[%d] (%s) is %T, want *string", i, f.Name, ptrs[i])
			}

			*p = f.Name
		case FieldBoolean:
			p, ok := ptrs[i].(*bool)
			if !ok {
				t.Fatalf("fieldPtrs[%d] (%s) is %T, want *bool", i, f.Name, ptrs[i])
			}

			*p = true
		default:
			t.Fatalf("field %d has unknown type %d", i, f.Type)
		}
	}

	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got) != DataFieldsPerRecord {
		t.Fatalf("marshaled %d keys, want %d", len(got), DataFieldsPerRecord)
	}

	for i, f := range FieldSchema {
		v, ok := got[f.Name]
		if !ok {
			t.Errorf("field %d (%s) missing from marshaled JSON", i, f.Name)

			continue
		}

		switch f.Type {
		case FieldInteger, FieldFloat:
			n, isNum := v.(float64)
			if !isNum || n != float64(i)+1 {
				t.Errorf("field %d (%s) = %v, want %v: the fieldPtrs index does not match the schema index",
					i, f.Name, v, float64(i)+1)
			}
		case FieldString:
			if v != f.Name {
				t.Errorf("field %d (%s) = %v, want %q", i, f.Name, v, f.Name)
			}
		case FieldBoolean:
			if v != true {
				t.Errorf("field %d (%s) = %v, want true", i, f.Name, v)
			}
		default:
			t.Errorf("field %d has unknown type %d", i, f.Type)
		}
	}
}
