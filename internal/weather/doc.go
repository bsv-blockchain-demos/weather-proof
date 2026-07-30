// Package weather encodes and decodes weather records as BSV locking scripts.
//
// The format is INTERNAL to this backend. Nothing outside this module decodes
// it: the browser only verifies a merkle proof over the raw transaction, and it
// reads the weather values from the JSON API. There is therefore no external
// byte contract to match, and no TypeScript oracle to agree with.
//
// What the format must still guarantee, and what this package enforces:
//
//   - a VALID script: OP_FALSE OP_RETURN, a version opcode, then 33 field
//     pushes in the fixed order of FieldSchema;
//   - a HARD 297-byte cap (MaxScriptBytes). This is fuel arithmetic, not style:
//     297 bytes is the top of the contiguous one-claim window at denomination
//     50, so one byte more silently costs a second fuel claim per record;
//   - Decode(Encode(x)) == x, because the app's own proof and reconciliation
//     paths read the script back;
//   - determinism: identical input, identical bytes, always. No map iteration
//     in the encode path, no wall clock, no randomness.
package weather
