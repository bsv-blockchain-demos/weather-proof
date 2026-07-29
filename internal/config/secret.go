// Package config holds process configuration.
//
// Only the Secret type lives here in this plan; the env parsing, Validate() and
// the trusted-proxy CIDR list are the read-API plan's first task. Secret is
// here rather than next to the DSN that first needed it because it must wrap
// SERVER_PRIVATE_KEY and TEMPEST_API_KEY as well as the Postgres password, and
// internal/api may never import internal/store/postgres — so defining it there
// would guarantee a hand-copied second copy, and the copy is the one that ends
// up missing a redaction method.
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

// LogValue implements slog.LogValuer with a redacted value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON implements json.Marshaler with a redacted value.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(redactedJSON), nil }

// Reveal returns the real value. This is the only way to read it.
func (s Secret) Reveal() string { return string(s) }
