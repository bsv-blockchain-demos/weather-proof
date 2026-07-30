package weather

import (
	"reflect"
	"slices"
	"testing"
)

func TestConstants(t *testing.T) {
	if Version != 1 {
		t.Errorf("Version = %d, want 1", Version)
	}

	if FloatScale != 1000000 {
		t.Errorf("FloatScale = %d, want 1000000", FloatScale)
	}

	if FloatEpsilon != 1e-6 {
		t.Errorf("FloatEpsilon = %v, want 1e-6", FloatEpsilon)
	}

	if DataFieldsPerRecord != 33 {
		t.Errorf("DataFieldsPerRecord = %d, want 33", DataFieldsPerRecord)
	}

	if ChunksPerRecord != 36 {
		t.Errorf("ChunksPerRecord = %d, want 36 (OP_FALSE, OP_RETURN, OP_1, 33 fields)", ChunksPerRecord)
	}

	if MaxScriptInt != 9007199254740991 {
		t.Errorf("MaxScriptInt = %d, want 9007199254740991 (2^53-1)", MaxScriptInt)
	}
}

func TestFieldTypeString(t *testing.T) {
	cases := map[FieldType]string{
		FieldInteger: "integer",
		FieldFloat:   "float",
		FieldString:  "string",
		FieldBoolean: "boolean",
		FieldType(9): "unknown",
	}

	for ft, want := range cases {
		if got := ft.String(); got != want {
			t.Errorf("FieldType(%d).String() = %q, want %q", ft, got, want)
		}
	}
}

// jsonTags returns the json tag of every WeatherData field, in declaration
// order. schema_test.go uses it too.
func jsonTags(t *testing.T) []string {
	t.Helper()

	rt := reflect.TypeOf(WeatherData{})

	tags := make([]string, 0, rt.NumField())

	for i := range rt.NumField() {
		tag, ok := rt.Field(i).Tag.Lookup("json")
		if !ok {
			t.Fatalf("WeatherData.%s has no json tag", rt.Field(i).Name)
		}

		tags = append(tags, tag)
	}

	return tags
}

func TestWeatherDataJSONTagsAreAlphabeticalAndComplete(t *testing.T) {
	tags := jsonTags(t)

	if len(tags) != DataFieldsPerRecord {
		t.Fatalf("WeatherData has %d fields, want %d", len(tags), DataFieldsPerRecord)
	}

	sorted := slices.Clone(tags)
	slices.Sort(sorted)

	for i := range tags {
		if tags[i] != sorted[i] {
			t.Errorf("json tag %d is %q but alphabetical order wants %q: declaration order is the wire order",
				i, tags[i], sorted[i])
		}
	}

	if tags[0] != "air_density" {
		t.Errorf("first json tag = %q, want air_density", tags[0])
	}

	if tags[len(tags)-1] != "wind_gust" {
		t.Errorf("last json tag = %q, want wind_gust", tags[len(tags)-1])
	}
}

// TestWeatherDataIsComparable pins the property the round-trip tests rely on:
// every field is a scalar, so two records can be compared with ==. Adding a
// slice, map or pointer field would break that silently.
func TestWeatherDataIsComparable(t *testing.T) {
	if !reflect.TypeOf(WeatherData{}).Comparable() {
		t.Fatal("WeatherData is no longer comparable: a field was added that is not a scalar")
	}
}
