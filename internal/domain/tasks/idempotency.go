package tasks

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

// PayloadFingerprint identifies a payload up to differences JSON itself treats
// as insignificant: object key order and whitespace. Two submits under one
// idempotency key are the same request exactly when their fingerprints are
// equal.
//
// It is the first 16 bytes of sha256 over the canonical form. A collision would
// accept a different payload as a duplicate, which is why it is 16 bytes and
// not 8.
type PayloadFingerprint [16]byte

// NewPayloadFingerprint canonicalizes payload and hashes it: the first 16
// bytes of sha256(canonicalJSON(payload)). It fails only when payload is not
// exactly one JSON value. The error wraps ErrInvalidPayload.
func NewPayloadFingerprint(payload json.RawMessage) (PayloadFingerprint, error) {
	p, err := canonicalJSON(payload)
	if err != nil {
		return [16]byte{}, err
	}

	h := sha256.Sum256(p)

	var fp [16]byte
	copy(fp[:], h[:16])

	return fp, nil
}

// IdempotencyRecord is what an idempotency key resolves to: the task the first
// submit created and the fingerprint of the payload that created it. The
// ledger row and the Redis value both store one.
type IdempotencyRecord struct {
	TaskID      uuid.UUID
	Fingerprint PayloadFingerprint
}

// canonicalJSON returns a byte-stable encoding of one JSON value: obejct keys
// sorted at every level, insignificant whitespace removed, string escapes
// normalized by the decode. Number literals are kept as sent.
//
// Three rules and each one is a test in idempotency_test.go:
//   - decode with json.Decoder and UseNumber, never json.Unmarshal into any,
//     wich turns every number into a float64;
//   - exactly one value: anything after it - a second value, stray bytes - is
//     ErrInvalidPayload, not silently igonred;
//   - encode what was decoded with json.Marshal, which sorts map keys at every
//     depth and writes a json.Number verbatim.
//
// This approximates RFC 8785 and is NOT conformant: keys sort by UTF-8 byte,
// not UTF-16 code unit and numbers are not re-formated. That is safe only
// because the output is compared solely against this function's own output.
func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: data after the first value", ErrInvalidPayload)
	}

	// encoding/json sorts map keys at every depth and writes a json.Number
	// verbatim, so marshalling what UseNumber decoded IS the canonical form.
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode canonical payload: %w", err)
	}
	return b, nil
}
