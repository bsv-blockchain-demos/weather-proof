package config

import (
	"log/slog"
)

// redacted is what a Secret renders as everywhere except Reveal.
const redacted = "[REDACTED]"

// redactedJSON is redacted as a JSON string, quoted once at compile time.
const redactedJSON = `"` + redacted + `"`

// Secret is a configuration value that must never appear in a log line, an
// error message or an HTTP response.
//
// It lives in this package rather than next to the DSN that first needed it
// because it must wrap SERVER_PRIVATE_KEY and TEMPEST_API_KEY as well as the
// Postgres password, and internal/api may never import
// internal/store/postgres — so defining it there would guarantee a hand-copied
// second copy, and the copy is the one that ends up missing a redaction method.
//
// All THREE of String, LogValue and MarshalJSON are required, and the third is
// the one that stops an exfiltration into a response BODY: encoding/json does
// not consult fmt.Stringer, so without MarshalJSON a Secret field inside any
// marshaled struct serializes the raw credential. Reveal is the single
// deliberate escape hatch and every call site is worth reading twice.
//
// Every method has a VALUE receiver on purpose. A pointer receiver would leave
// the value-field case unprotected — json.Marshal of a struct holding a
// non-addressable Secret would not find the method — and recvcheck forbids
// mixing the two.
type Secret string

// String implements fmt.Stringer with a redacted value.
func (s Secret) String() string { return redacted }

// GoString implements fmt.GoStringer with a redacted value.
//
// The %#v verb consults GoStringer, not Stringer — a Secret with only String
// still prints its raw value under %#v, bare or as a struct field, because
// %#v does not fall back to String when GoString is absent.
func (s Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer with a redacted value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON implements json.Marshaler with a redacted value.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(redactedJSON), nil }

// MarshalText implements encoding.TextMarshaler with a redacted value.
//
// This closes the leak for any encoder that consults TextMarshaler for a
// VALUE — YAML, TOML, an XML attribute, a log formatter — and is the
// technically correct implementation for the interface regardless.
//
// It does NOT close every map-key case in encoding/json specifically:
// encoding/json's resolveKeyName (encoding/json/encode.go) special-cases any
// map key whose reflect.Kind is String and returns the raw string directly,
// UNCONDITIONALLY, before it ever checks TextMarshaler. Because Secret's
// underlying type is string, a map[Secret]V key still leaks through
// json.Marshal despite this method — verified against this toolchain's
// GOROOT source and covered by
// TestSecretAsJSONMapKeyIsAKnownGoLimitation in secret_test.go, which
// documents the gap rather than pretending it is closed. Closing it fully
// would require Secret to stop being Kind==String (e.g. a wrapping struct),
// which would break every existing config.Secret("literal") conversion in
// this codebase and is out of scope here.
//
// There is deliberately no UnmarshalText: this type has exactly one way in
// (Secret(rawValue), a plain conversion at the point config is parsed) and
// adding a text-unmarshal path would let a redacted "[REDACTED]" round-trip
// back in as though it were the real credential — a silent corruption that is
// worse than the leak this method closes.
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Reveal returns the real value. This is the only way to read it.
func (s Secret) Reveal() string { return string(s) }
