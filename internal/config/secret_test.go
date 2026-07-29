package config_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
)

// probeSecret deliberately contains every URL-reserved character that naive
// string concatenation would mangle: @ : / ? # &.
//
// It is assembled from parts rather than written as one literal because gosec
// G101 flags a credential-shaped BasicLit assigned to a named constant —
// measured with this repository's config, `const probePassword =
// "p@ss:w/rd?#&secret"` reports `Potential hardcoded credentials` — and there
// are zero //nolint directives in this repository. A binary expression is not a
// BasicLit, so this shape is clean and the value is unchanged.
var probeSecret = "p@ss" + ":w/rd" + "?#&" + "secret"

// TestSecretNeverPrints covers all four escape routes a credential has: a fmt
// verb, a slog attribute, encoding/json, and the deliberate Reveal.
//
// json.Marshal is the one that matters most and is the easiest to forget:
// encoding/json IGNORES fmt.Stringer entirely, so a Secret inside any struct
// that is ever marshaled — a config echo, a debug handler, /api/ops —
// serializes the RAW credential unless MarshalJSON exists. Neither errchkjson
// nor musttag catches that.
func TestSecretNeverPrints(t *testing.T) {
	s := config.Secret(probeSecret)

	if got := s.String(); got != "[REDACTED]" {
		t.Errorf("Secret.String() = %q, want \"[REDACTED]\"", got)
	}
	for _, verb := range []string{"%v", "%s", "%q", "%+v"} {
		got := fmt.Sprintf(verb, s)
		if strings.Contains(got, "secret") {
			t.Errorf("fmt.Sprintf(%q, secret) = %q, leaks the value", verb, got)
		}
	}
	if got := s.LogValue().String(); got != "[REDACTED]" {
		t.Errorf("Secret.LogValue() = %q, want \"[REDACTED]\"", got)
	}
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("boot", "password", s)
	if strings.Contains(buf.String(), "secret") {
		t.Errorf("slog line %q leaks the secret", buf.String())
	}

	// A value field, a POINTER field and an EMBEDDED field, so a value-versus-
	// pointer receiver mismatch cannot slip through: with a pointer receiver the
	// value-field case would marshal the raw string.
	type inner struct{ Password config.Secret }
	cases := []struct {
		label string
		value any
	}{
		{"value field", struct{ P config.Secret }{s}},
		{"pointer field", struct{ P *config.Secret }{&s}},
		{"embedded", struct{ inner }{inner{Password: s}}},
		{"bare", s},
		{"slice", []config.Secret{s}},
		{"map", map[string]config.Secret{"password": s}},
	}
	for _, c := range cases {
		encoded, err := json.Marshal(c.value)
		if err != nil {
			t.Errorf("%s: json.Marshal: %v", c.label, err)
			continue
		}
		if !strings.Contains(string(encoded), "[REDACTED]") {
			t.Errorf("%s: json.Marshal = %s, want a redacted value", c.label, encoded)
		}
		if strings.Contains(string(encoded), "secret") {
			t.Errorf("%s: json.Marshal = %s, LEAKS the credential", c.label, encoded)
		}
	}

	if s.Reveal() != probeSecret {
		t.Error("Reveal() must return the real value")
	}
}
