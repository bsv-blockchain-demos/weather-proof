package weather

import (
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/script/interpreter"
)

// TestAppendScriptNumTable pins the number encoding. Every hex string here was
// measured against go-sdk v1.3.2; if any of them changes, the wire format has
// changed and every published record has become unreadable.
func TestAppendScriptNumTable(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "00"},                           // OP_0
		{-1, "4f"},                          // OP_1NEGATE
		{1, "51"},                           // OP_1
		{2, "52"},                           // OP_2
		{16, "60"},                          // OP_16, the last opcode branch
		{17, "0111"},                        // the first data push
		{127, "017f"},                       // largest single byte with no sign bit
		{128, "028000"},                     // needs a sign-extension byte
		{-128, "028080"},                    // sign-extension byte set to 0x80
		{255, "02ff00"},                     //
		{256, "020001"},                     // little-endian
		{-256, "020081"},                    //
		{-127, "01ff"},                      // sign bit folded into the top byte
		{2147483647, "04ffffff7f"},          // max int32
		{2147483648, "050000008000"},        // one past it - five bytes, not four
		{-2147483648, "050000008080"},       //
		{MaxScriptInt, "07ffffffffffff1f"},  // 2^53-1
		{-MaxScriptInt, "07ffffffffffff9f"}, //
	}

	for _, tc := range cases {
		s := &script.Script{}
		if err := appendScriptNum(s, tc.n); err != nil {
			t.Errorf("appendScriptNum(%d) returned %v, want nil", tc.n, err)

			continue
		}

		if got := s.String(); got != tc.want {
			t.Errorf("appendScriptNum(%d) = %s, want %s", tc.n, got, tc.want)
		}
	}
}

// TestAppendScriptNumEmitsAllSixteenOpcodes walks 1..16 so an off-by-one in
// script.Op1 + byte(n) - 1 cannot hide.
func TestAppendScriptNumEmitsAllSixteenOpcodes(t *testing.T) {
	for n := int64(1); n <= 16; n++ {
		s := &script.Script{}
		if err := appendScriptNum(s, n); err != nil {
			t.Fatalf("appendScriptNum(%d): %v", n, err)
		}

		b := s.Bytes()
		if len(b) != 1 {
			t.Fatalf("appendScriptNum(%d) emitted %d bytes, want 1", n, len(b))
		}

		want := byte(0x50) + byte(n)
		if b[0] != want {
			t.Errorf("appendScriptNum(%d) = %#02x, want %#02x", n, b[0], want)
		}
	}
}

func TestAppendScriptNumOutOfRange(t *testing.T) {
	for _, n := range []int64{MaxScriptInt + 1, -MaxScriptInt - 1} {
		s := &script.Script{}

		err := appendScriptNum(s, n)
		if !errors.Is(err, ErrNumberOutOfRange) {
			t.Errorf("appendScriptNum(%d) error = %v, want ErrNumberOutOfRange", n, err)
		}

		if len(*s) != 0 {
			t.Errorf("appendScriptNum(%d) wrote %d bytes on failure, want 0", n, len(*s))
		}
	}
}

// TestScriptNumberBytesMutatesItsReceiver documents the go-sdk gotcha that
// forces appendScriptNum to build a fresh ScriptNumber on every call, and
// guards against a regression to a cached-and-reused ScriptNumber. If a
// future SDK release fixes the raw mutation, the first section below fails,
// which is the correct outcome: the comment in scriptnum.go then needs
// updating.
//
// The guard cannot merely compare two calls' outputs to each other. Under a
// cache-and-reuse bug (one *ScriptNumber memoized per value and reused across
// calls to appendScriptNum), the FIRST call anywhere in the whole test binary
// for a given value returns the CORRECT bytes - the mutation is a side effect
// of that call, not a precondition for it - and every call after that, in any
// test, returns the SAME wrong bytes. If another test earlier in this file
// (alphabetically, or by declaration order) already called
// appendScriptNum(-128), the cache is already poisoned before this test's
// first call runs, so both of this test's calls would return the identical
// wrong answer and an equality check between them would pass. Instead, each
// call here is checked against the literal known-correct encoding, so a
// poisoned cache is caught on whichever call first observes it, independent
// of what ran before this test or in what order tests run.
func TestScriptNumberBytesMutatesItsReceiver(t *testing.T) {
	// The raw go-sdk behavior this guard exists because of: Bytes() mutates
	// its receiver on a negative value, so calling it twice on the SAME
	// ScriptNumber gives a different (wrong) answer the second time.
	sn := &interpreter.ScriptNumber{Val: big.NewInt(-128), AfterGenesis: true}

	rawFirst := hex.EncodeToString(sn.Bytes())
	if rawFirst != "8080" {
		t.Fatalf("raw ScriptNumber(-128).Bytes() first call = %s, want 8080", rawFirst)
	}

	rawSecond := hex.EncodeToString(sn.Bytes())
	if rawSecond != "8000" {
		t.Fatalf("raw ScriptNumber(-128).Bytes() second call = %s, want 8000 (mutation not reproduced; did go-sdk change?)",
			rawSecond)
	}

	if sn.Val.Int64() != 128 {
		t.Fatalf("raw ScriptNumber(-128).Val after two Bytes() calls = %v, want 128", sn.Val)
	}

	// The property appendScriptNum must actually have: encoding -128 gives
	// the correct bytes EVERY time, checked against ground truth rather than
	// against a sibling call, so a cached-and-reused ScriptNumber cannot hide
	// behind two equally wrong answers.
	for call := range 3 {
		s := &script.Script{}

		appendErr := appendScriptNum(s, -128)
		if appendErr != nil {
			t.Fatalf("call %d: appendScriptNum(-128): %v", call, appendErr)
		}

		if got := s.String(); got != "028080" {
			t.Fatalf("call %d: appendScriptNum(-128) = %s, want 028080 (a ScriptNumber is being cached and reused across calls)",
				call, got)
		}
	}
}

// TestScriptNumRoundTrip is the property that makes the decoder possible.
func TestScriptNumRoundTrip(t *testing.T) {
	values := []int64{
		0, 1, -1, 2, -2, 15, 16, 17, -16, -17, 127, -127, 128, -128, 255, -255,
		256, -256, 32767, -32768, 65535, 16777215, 2147483647, -2147483648,
		4294967296, MaxScriptInt, -MaxScriptInt,
	}

	for _, n := range values {
		s := &script.Script{}
		if err := appendScriptNum(s, n); err != nil {
			t.Fatalf("appendScriptNum(%d): %v", n, err)
		}

		chunks, err := script.DecodeScript(s.Bytes(), script.DecodeOptionsParseOpReturn)
		if err != nil {
			t.Fatalf("DecodeScript(%s): %v", s.String(), err)
		}

		if len(chunks) != 1 {
			t.Fatalf("appendScriptNum(%d) produced %d chunks, want 1", n, len(chunks))
		}

		got, err := scriptNumFromChunk(chunks[0])
		if err != nil {
			t.Fatalf("scriptNumFromChunk(%s): %v", s.String(), err)
		}

		if got != n {
			t.Errorf("round trip of %d gave %d (via %s)", n, got, s.String())
		}
	}
}

func TestScriptNumFromChunkRejectsNonNumbers(t *testing.T) {
	cases := []struct {
		name  string
		chunk *script.ScriptChunk
	}{
		{"OP_RETURN", &script.ScriptChunk{Op: script.OpRETURN}},
		{"OP_DUP", &script.ScriptChunk{Op: script.OpDUP}},
		{"OP_RESERVED", &script.ScriptChunk{Op: 0x50}},
		{"non-minimal 7f00", &script.ScriptChunk{Op: script.OpDATA2, Data: []byte{0x7f, 0x00}}},
		{"negative zero 80", &script.ScriptChunk{Op: script.OpDATA1, Data: []byte{0x80}}},
		{"nine bytes", &script.ScriptChunk{
			Op:   script.OpDATA9,
			Data: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9},
		}},
	}

	for _, tc := range cases {
		if _, err := scriptNumFromChunk(tc.chunk); err == nil {
			t.Errorf("scriptNumFromChunk(%s) returned nil error, want a rejection", tc.name)
		}
	}
}

// TestScriptNumFromChunkRejectsOversizedMagnitude covers the 8-byte push that is
// short enough for MakeScriptNumber but larger than MaxScriptInt.
func TestScriptNumFromChunkRejectsOversizedMagnitude(t *testing.T) {
	// 0xffffffffffffff7f little-endian = 2^63-1, minimally encoded.
	chunk := &script.ScriptChunk{
		Op:   script.OpDATA8,
		Data: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f},
	}

	_, err := scriptNumFromChunk(chunk)
	if !errors.Is(err, ErrNumberOutOfRange) {
		t.Errorf("error = %v, want ErrNumberOutOfRange", err)
	}
}
