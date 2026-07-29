package weather

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
)

var (
	// ErrMalformedScript is returned for anything that is not the layout Encode
	// writes: a missing prefix, too few chunks, or a chunk that is not a legal
	// value for its field.
	ErrMalformedScript = errors.New("malformed weather script")

	// ErrUnsupportedVersion is returned when the version opcode is not Version.
	ErrUnsupportedVersion = errors.New("unsupported weather record version")
)

// Decode parses a weather locking script back into a record.
//
// EXACTLY ONE LAYOUT is supported: the one Encode writes. There is deliberately
// no tolerance for historical layouts. Records in older layouts do exist on
// chain but are unlocatable - the database that held their txids is gone - so
// multi-layout support would be untestable code guarding against input that
// cannot arrive. It is also a live hazard: two of the historical layouts are
// pure permutations of the same 33 chunks, so a mis-selected layout decodes to
// plausible garbage instead of an error.
//
// Trailing chunks after the 33 fields are tolerated and ignored. Fewer than
// ChunksPerRecord chunks is a hard rejection.
func Decode(s *script.Script) (*WeatherData, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil script", ErrMalformedScript)
	}

	// DecodeOptionsParseOpReturn is REQUIRED. Without it go-sdk's DecodeScript
	// sets op.Data to the whole remainder of the script including the 0x6a byte
	// and stops, so a weather script parses as 2 chunks instead of 36. Verified
	// at v1.3.2: 006a5101ff0102 yields 5 chunks with the option and 2 without.
	chunks, err := script.DecodeScript(s.Bytes(), script.DecodeOptionsParseOpReturn)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedScript, err)
	}

	// Checked against 3+len(FieldSchema) directly, not the ChunksPerRecord
	// constant: the loop below indexes chunks[3+i] for i up to len(FieldSchema),
	// so this is the actual invariant that keeps that indexing in bounds even if
	// FieldSchema and DataFieldsPerRecord (which ChunksPerRecord is derived from)
	// were ever to drift apart.
	if len(chunks) < 3+len(FieldSchema) {
		return nil, fmt.Errorf("%w: %d chunks, want at least %d",
			ErrMalformedScript, len(chunks), 3+len(FieldSchema))
	}

	if chunks[0].Op != script.OpFALSE || chunks[1].Op != script.OpRETURN {
		return nil, fmt.Errorf("%w: want an OP_FALSE OP_RETURN prefix, got %#02x %#02x",
			ErrMalformedScript, chunks[0].Op, chunks[1].Op)
	}

	version, err := scriptNumFromChunk(chunks[2])
	if err != nil {
		return nil, fmt.Errorf("%w: version chunk: %w", ErrMalformedScript, err)
	}

	if version != Version {
		return nil, fmt.Errorf("%w: %d, want %d", ErrUnsupportedVersion, version, Version)
	}

	// The version chunk's push framing is pinned, unlike data fields. Encode
	// always emits the bare OP_1 opcode for version 1, never a data push of the
	// byte 0x01 (e.g. OP_DATA1 0x01, or OP_PUSHDATA1 0x01 0x01) - both of which
	// decode to the same numeric value and would pass the check above. readField
	// deliberately tolerates that kind of non-minimal push framing in ordinary
	// data fields; the version identifies the record layout, so it is the one
	// place worth pinning to the exact opcode Encode writes.
	if chunks[2].Op != script.Op1 {
		return nil, fmt.Errorf("%w: version chunk is opcode %#02x, want the bare %#02x (OP_1)",
			ErrMalformedScript, chunks[2].Op, script.Op1)
	}

	d := &WeatherData{}
	ptrs := d.fieldPtrs()

	for i, f := range FieldSchema {
		if err := readField(chunks[3+i], f, ptrs[i]); err != nil {
			return nil, fmt.Errorf("%w: field %d (%s): %w", ErrMalformedScript, i, f.Name, err)
		}
	}

	return d, nil
}

// DecodeHex is Decode over a hex string.
func DecodeHex(h string) (*WeatherData, error) {
	raw, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedScript, err)
	}

	return Decode(script.NewFromBytes(raw))
}

// IsValidScript reports whether s decodes as a weather record. It is the cheap
// gate for "is this output one of ours" and never returns a partial record. A
// nil s (an output whose LockingScript was never set) reports false rather than
// panicking, because callers scan every output of a transaction, most of which
// are not this package's.
func IsValidScript(s *script.Script) bool {
	_, err := Decode(s)

	return err == nil
}

// readField writes one chunk into the struct field ptr points at.
func readField(c *script.ScriptChunk, f FieldDefinition, ptr any) error {
	switch f.Type {
	case FieldInteger:
		v, ok := ptr.(*int64)
		if !ok {
			return fmt.Errorf("%w: %s is integer but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		n, err := scriptNumFromChunk(c)
		if err != nil {
			return err
		}

		*v = n

		return nil
	case FieldFloat:
		v, ok := ptr.(*float64)
		if !ok {
			return fmt.Errorf("%w: %s is float but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		n, err := scriptNumFromChunk(c)
		if err != nil {
			return err
		}

		*v = unscaleFloat(n)

		return nil
	case FieldString:
		v, ok := ptr.(*string)
		if !ok {
			return fmt.Errorf("%w: %s is string but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		// Everything from OP_0 (0x00, the empty push) up to OP_PUSHDATA4 (0x4e)
		// is a data push. Anything above it is an opcode, which in a string
		// position means a forged or corrupt script - including 0x6a.
		if c.Op > script.OpPUSHDATA4 {
			return fmt.Errorf("%w: opcode %#02x is not a data push", ErrMalformedScript, c.Op)
		}

		*v = string(c.Data)

		return nil
	case FieldBoolean:
		v, ok := ptr.(*bool)
		if !ok {
			return fmt.Errorf("%w: %s is boolean but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		n, err := scriptNumFromChunk(c)
		if err != nil {
			return err
		}

		*v = n != 0

		return nil
	default:
		return fmt.Errorf("%w: %s has type %d", ErrUnknownFieldType, f.Name, f.Type)
	}
}
