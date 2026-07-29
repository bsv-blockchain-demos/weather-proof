package weather

import (
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/script"
)

// MaxScriptBytes is a HARD cap on the encoded script, and it is money.
//
// 297 bytes is the top of the CONTIGUOUS one-claim fuel window at denomination
// 50: a script one byte longer silently costs a second fuel claim for every
// record published, forever. It is not 331 (the top of a disjoint island - the
// 298..321 gap in between already costs two claims) and not 250 (an earlier
// figure that wastes 47 bytes of usable window). The number must be re-derived
// whenever the denomination changes, never copied.
//
// Headroom: the real weather sample encodes to 99 bytes and the repository's own
// adversarial fixture to 211 bytes. The cap exists because conditions, icon,
// pressure_trend and lightning_strike_last_distance_msg are FREE-FORM strings,
// not enums, so nothing else bounds the script.
const MaxScriptBytes = 297

// maxPushDataLen is the largest string a single OP_PUSHDATA2 can carry. Above it
// go-sdk would emit OP_PUSHDATA4, which this format does not use.
const maxPushDataLen = 0xFFFF

var (
	// ErrScriptTooLarge is returned when the finished script exceeds
	// MaxScriptBytes. The publisher consumes this to mark the row terminally
	// failed instead of paying two fuel claims.
	ErrScriptTooLarge = errors.New("script exceeds the maximum size")

	// ErrStringTooLong is returned for a string field above maxPushDataLen. The
	// MaxScriptBytes cap makes it unreachable in practice; it is a defensive
	// guard so the format can never silently grow an OP_PUSHDATA4.
	ErrStringTooLong = errors.New("string field exceeds the maximum push size")

	// ErrUnknownFieldType is returned if FieldSchema ever grows a type this
	// encoder does not handle. It cannot be triggered by input data.
	ErrUnknownFieldType = errors.New("unknown field type in schema")
)

// Encode serializes d as a weather locking script:
//
//	00 6a        OP_FALSE OP_RETURN - provably unspendable, so the output adds
//	             no UTXO to any basket
//	51           the version opcode (OP_1)
//	...          the 33 fields, in FieldSchema order
//
// It is a pure function of d: it reads no clock, consumes no randomness and
// never ranges over a map, so the same record always produces the same bytes.
//
// It returns ErrScriptTooLarge, ErrNonFinite, ErrNumberOutOfRange or
// ErrStringTooLong rather than approximate bytes. There is no path that returns
// a script the cap forbids.
func Encode(d *WeatherData) (*script.Script, error) {
	s := &script.Script{}

	// AppendOpcodes rejects 0x01..0x4e, so it cannot be used for pushes - but
	// OP_FALSE (0x00), OP_RETURN (0x6a) and OP_1 (0x51) are all outside that
	// range and are appended verbatim.
	if err := s.AppendOpcodes(script.OpFALSE, script.OpRETURN, script.Op1); err != nil {
		return nil, fmt.Errorf("append prefix: %w", err)
	}

	ptrs := d.fieldPtrs()

	for i, f := range FieldSchema {
		if err := appendField(s, f, ptrs[i]); err != nil {
			return nil, fmt.Errorf("field %d (%s): %w", i, f.Name, err)
		}
	}

	if len(*s) > MaxScriptBytes {
		return nil, fmt.Errorf("%w: %d bytes > cap %d", ErrScriptTooLarge, len(*s), MaxScriptBytes)
	}

	return s, nil
}

// EncodeHex is Encode returning the lowercase hex of the script.
func EncodeHex(d *WeatherData) (string, error) {
	s, err := Encode(d)
	if err != nil {
		return "", err
	}

	return s.String(), nil
}

// appendField emits one field. ptr is the corresponding entry of fieldPtrs, so
// its concrete type is guaranteed by f.Type; a mismatch means schema.go and
// types.go have drifted and is reported as ErrUnknownFieldType rather than
// panicking.
func appendField(s *script.Script, f FieldDefinition, ptr any) error {
	switch f.Type {
	case FieldInteger:
		v, ok := ptr.(*int64)
		if !ok {
			return fmt.Errorf("%w: %s is integer but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		return appendScriptNum(s, *v)
	case FieldFloat:
		v, ok := ptr.(*float64)
		if !ok {
			return fmt.Errorf("%w: %s is float but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		n, err := scaleFloat(*v)
		if err != nil {
			return err
		}

		return appendScriptNum(s, n)
	case FieldString:
		v, ok := ptr.(*string)
		if !ok {
			return fmt.Errorf("%w: %s is string but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		if len(*v) > maxPushDataLen {
			return fmt.Errorf("%w: %d bytes > %d", ErrStringTooLong, len(*v), maxPushDataLen)
		}

		// AppendPushData, never appendScriptNum: a string field must always be a
		// length-prefixed data push. Minimizing a one-byte string whose byte is
		// 0x01..0x10 into OP_1..OP_16 would produce a chunk with no Data, and the
		// value would decode as the empty string. Measured at v1.3.2:
		// AppendPushData emits 00 for empty, 0x01..0x4b + data for 1..75 bytes,
		// 4c <len> for 76..255 and 4d <len:2 LE> for 256..65535.
		return s.AppendPushData([]byte(*v))
	case FieldBoolean:
		v, ok := ptr.(*bool)
		if !ok {
			return fmt.Errorf("%w: %s is boolean but fieldPtrs gave %T", ErrUnknownFieldType, f.Name, ptr)
		}

		n := int64(0)
		if *v {
			n = 1
		}

		return appendScriptNum(s, n)
	default:
		return fmt.Errorf("%w: %s has type %d", ErrUnknownFieldType, f.Name, f.Type)
	}
}
