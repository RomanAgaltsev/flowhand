package tasks

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	// maxNumberBytes bounds one JSON number in a payload. Postgres stores a JSONB
	// number as numeric, which refuses more than 16383 digits after the point, so
	// a number cannot be left to grow with the body. A double needs 24 characters;
	// 1024 leaves room for exact decimals without approaching that limit.
	maxNumberBytes = 1024

	// maxExponentDigits bounds a number's exponent for the same reason: numeric
	// refuses 1e-16384 although a double reads it as a plain 0.
	maxExponentDigits = 3
)

// PayloadFingerprint identifies a payload up to differences JSON itself treats
// as insignificant: object member order, whitespace, string escapes and the
// spelling of a number. Two submits under one idempotency key are the same
// request exactly when their fingerprints are equal.
//
// It is the first 16 bytes of sha256 over the canonical form. A collision would
// accept a different payload as a duplicate, which is why it is 16 bytes and
// not 8.
type PayloadFingerprint [16]byte

// NewPayloadFingerprint canonicalizes payload and hashes it: the first 16
// bytes of sha256(canonicalJSON(payload)). It fails when payload is not a value
// the task store can hold; the error wraps ErrInvalidPayload.
func NewPayloadFingerprint(payload json.RawMessage) (PayloadFingerprint, error) {
	p, err := canonicalJSON(payload)
	if err != nil {
		return PayloadFingerprint{}, err
	}

	h := sha256.Sum256(p)

	var fp PayloadFingerprint
	copy(fp[:], h[:])

	return fp, nil
}

// IdempotencyRecord is what an idempotency key resolves to: the task the first
// submit created and the fingerprint of the payload that created it. The
// ledger row and the Redis value both store one.
type IdempotencyRecord struct {
	TaskID      uuid.UUID
	Fingerprint PayloadFingerprint
}

// IdempotencyClaim is what a keyed submit asserts: this key, for this payload.
// It is built once, in the Commander, and stored as given, so the cache and the
// ledger can never hold two different fingerprints for one submit.
type IdempotencyClaim struct {
	Key         IdempotencyKey
	Fingerprint PayloadFingerprint
}

// canonicalJSON returns the RFC 8785 (JCS) form of one JSON value, or
// ErrInvalidPayload when raw is anything else: trailing data, invalid UTF-8, a
// lone surrogate, a duplicate object name, or a value checkStorable refuses.
//
// Integers keep their digits (CanonicalizeRawInts(false)), so ids beyond 2^53
// stay distinct. Every other number becomes an IEEE 754 double, which makes 1.0
// and 1 one request, and makes two floats that differ only past the 17th
// significant digit one request too: accepted, because a task payload that
// depends on that precision is carrying it as a string.
func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	v := jsontext.Value(bytes.Clone(raw)) // Canonicalize works in place
	if err := v.Canonicalize(jsontext.CanonicalizeRawInts(false)); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	// raw, not v: Canonicalize saturates a float beyond double range, so the
	// out-of-range number checkStorable looks for is gone from v.
	if err := checkStorable(raw); err != nil {
		return nil, err
	}
	return v, nil
}

// checkStorable refuses what is valid JSON but cannot go into a Postgres JSONB
// column: U+0000 in a string or an object name, and a number numeric cannot
// hold. Without it those payloads reach the insert and come back as a 500.
//
// raw must already be one valid value; canonicalJSON establishes that first.
func checkStorable(raw json.RawMessage) error {
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.ReadToken()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidPayload, err)
		}

		switch tok.Kind() {
		case jsontext.KindString:
			if strings.IndexByte(tok.String(), 0) >= 0 {
				return fmt.Errorf("%w: contains U+0000", ErrInvalidPayload)
			}
		case jsontext.KindNumber:
			if !numberFits(tok.String()) {
				return fmt.Errorf("%w: a number is out of range", ErrInvalidPayload)
			}
		default:
			// Structure and literals hold nothing the column can refuse.
		}
	}
}

// numberFits reports whether a JSON number literal is inside the bounds a
// payload may use. Beyond double range is refused as well as beyond numeric's:
// canonicalization would otherwise saturate 1e400 and 1e500 to one value, and
// two different payloads would share a fingerprint.
func numberFits(s string) bool {
	if len(s) > maxNumberBytes {
		return false
	}
	if _, exp, ok := strings.Cut(strings.ToLower(s), "e"); ok &&
		len(strings.TrimLeft(exp, "+-0")) > maxExponentDigits {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}
