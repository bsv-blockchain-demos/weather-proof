package weather

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/script/interpreter"
)

// ErrNumberOutOfRange is returned for a value whose magnitude exceeds
// MaxScriptInt, in either direction.
var ErrNumberOutOfRange = errors.New("number out of encodable range")

// ErrNotANumber is returned when a chunk in a numeric field position is not a
// number push or a small-integer opcode.
var ErrNotANumber = errors.New("chunk is not a number")

// appendScriptNum appends n to s using MINIMAL push encoding.
//
// The three opcode branches come first because minimal push encoding is a
// script-validity rule, and they are also why a small value costs one byte
// instead of two:
//
//	n == 0            -> OP_0        (0x00)
//	n == -1           -> OP_1NEGATE  (0x4f)
//	1 <= n <= 16      -> OP_1..OP_16 (0x51..0x60)
//	anything else     -> a data push of the little-endian sign-magnitude bytes
//
// The serialization for the last branch is NOT hand-rolled. go-sdk already
// implements exactly it in script/interpreter, and its output was measured at
// v1.3.2: 17 -> 0111, 127 -> 017f, 128 -> 028000, -128 -> 028080,
// 2147483647 -> 04ffffff7f, 9007199254740991 -> 07ffffffffffff1f.
//
// GOTCHA, verified at v1.3.2: (*interpreter.ScriptNumber).Bytes() calls n.Neg()
// on a negative value and never restores the sign, so it MUTATES its receiver.
// Measured: a ScriptNumber holding -128 returns 8080 on the first call and 8000
// on the second, with Val left at +128. A fresh ScriptNumber is therefore
// constructed on every call - never cache one, never Set() and reuse it.
//
// AfterGenesis is set explicitly true. At v1.3.2 it only sizes a slice, but the
// !AfterGenesis branch clamps to int32 and a future release could make that
// load-bearing; the intent here is full-width values.
//
// Do NOT use (*script.Script).AppendBigInt: it is AppendPushData(bInt.Bytes()),
// which is big-endian magnitude with NO sign byte. Measured: it encodes -128 as
// 0180, i.e. as +128. Undecodable.
func appendScriptNum(s *script.Script, n int64) error {
	if n > MaxScriptInt || n < -MaxScriptInt {
		return fmt.Errorf("%w: %d", ErrNumberOutOfRange, n)
	}

	switch {
	case n == 0:
		return s.AppendOpcodes(script.Op0)
	case n == -1:
		return s.AppendOpcodes(script.Op1NEGATE)
	case n >= 1 && n <= 16:
		// n is in [1,16] so the sum is in [0x51,0x60]; it cannot overflow.
		return s.AppendOpcodes(script.Op1 + byte(n) - 1)
	default:
		sn := &interpreter.ScriptNumber{Val: big.NewInt(n), AfterGenesis: true}

		return s.AppendPushData(sn.Bytes())
	}
}

// scriptNumFromChunk is the exact inverse of appendScriptNum.
//
// It reads the small-integer opcodes from the opcode byte itself, because those
// chunks carry no Data, and everything else through
// interpreter.MakeScriptNumber with requireMinimal set. Requiring minimal
// encoding is what makes a hand-forged script fail loudly: 7f00 is rejected as
// non-minimal even though it decodes numerically to 127.
func scriptNumFromChunk(c *script.ScriptChunk) (int64, error) {
	switch {
	case c.Op == script.Op0:
		return 0, nil
	case c.Op == script.Op1NEGATE:
		return -1, nil
	case c.Op >= script.Op1 && c.Op <= script.Op16:
		return int64(c.Op-script.Op1) + 1, nil
	case c.Op >= script.OpDATA1 && c.Op <= script.OpPUSHDATA4:
		// scriptNumLen 8 admits the 7 bytes that 2^53-1 needs plus a sign byte.
		//
		// This also makes (*interpreter.ScriptNumber).Int64()'s clamp-on-overflow
		// PROVABLY UNREACHABLE here: at 8 bytes the largest sign-magnitude value
		// requireMinimal will accept is 2^63-1 (ffffffffffffff7f), which Int64()
		// returns exactly. The next value up, 2^63 as [00,00,00,00,00,00,00,80],
		// is rejected by requireMinimal as non-minimally encoded before Int64()
		// ever sees it - verified against go-sdk v1.3.2. A future change to
		// scriptNumLen must re-derive this, or the clamping hazard can silently
		// return.
		sn, err := interpreter.MakeScriptNumber(c.Data, 8, true, true)
		if err != nil {
			return 0, fmt.Errorf("%w: %w", ErrNotANumber, err)
		}

		n := sn.Int64()
		if n > MaxScriptInt || n < -MaxScriptInt {
			return 0, fmt.Errorf("%w: %d", ErrNumberOutOfRange, n)
		}

		return n, nil
	default:
		return 0, fmt.Errorf("%w: opcode %#02x", ErrNotANumber, c.Op)
	}
}
