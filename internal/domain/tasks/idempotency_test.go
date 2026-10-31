package tasks

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalJSON(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		expected string
	}{
		// Object key order is insignificant, at every level.
		{"keys sorted at every level", `{"b":2,"a":1,"c":{"y":2,"x":1}}`, `{"a":1,"b":2,"c":{"x":1,"y":2}}`},
		// The case a non-recursive implementation gets wrong: the outer value is
		// an array, so nothing sorts the keys of the object inside it.
		{"object inside array is sorted", `[{"b":1,"a":2}]`, `[{"a":2,"b":1}]`},
		// Array order IS the data. Sorting it would make [1,2] equal [2,1].
		{"array order is kept", `[2,1]`, `[2,1]`},
		{"whitespace removed", "{\n  \"a\" : 1,\n  \"b\" : [1, 2]\n}", `{"a":1,"b":[1,2]}`},
		// Without dec.UseNumber() this decodes to float64 and comes back as
		// 12345678901234567000 - and two payloads differing in the last digits
		// would fingerprint identically.
		{"big integer survives", `{"n":12345678901234567890}`, `{"n":12345678901234567890}`},
		// Numbers are kept as the client sent them. Documented limitation: 1 and
		// 1.0 are different fingerprints. Re-formatting numbers is the part of
		// RFC 8785 this does not attempt.
		{"number literal kept as sent", `{"n":1.0}`, `{"n":1.0}`},
		// The decode normalizes string escapes, so "\u0041" and "A" agree.
		{"string escapes normalized", `{"s":"\u0041"}`, `{"s":"A"}`},
		{"literals", `[true,false,null]`, `[true,false,null]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := canonicalJSON(json.RawMessage(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.expected, string(got))
		})
	}
}

func TestCanonicalJSON_RejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"truncated", `{"a":`},
		// json.Decoder stops after one value and would happily ignore the rest.
		// A payload that is two values is not one payload.
		{"trailing value", `{"a":1}{"b":2}`},
		{"trailing garbage", `{"a":1} x`},
		{"empty", ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := canonicalJSON(json.RawMessage(tt.in))
			require.ErrorIs(t, err, ErrInvalidPayload)
		})
	}
}

func TestNewPayloadFingerprint(t *testing.T) {
	a, err := NewPayloadFingerprint(json.RawMessage(`{"b":[1,{"y":1,"x":2}],"a":"s"}`))
	require.NoError(t, err)
	b, err := NewPayloadFingerprint(json.RawMessage(`{ "a":"s", "b":[1,{"x":2,"y":1}] }`))
	require.NoError(t, err)
	c, err := NewPayloadFingerprint(json.RawMessage(`{"a":"s","b":[{"x":2,"y":1},1]}`))
	require.NoError(t, err)

	assert.Equal(t, a, b, "reordered keys and whitespace are the same request")
	assert.NotEqual(t, a, c, "a reordered array is a different request")
	assert.NotEqual(t, PayloadFingerprint{}, a, "an all-zero fingerprint means nothing was hashed")

	_, err = NewPayloadFingerprint(json.RawMessage(`{"a":`))
	require.ErrorIs(t, err, ErrInvalidPayload)
}

// TestNewPayloadFingerprint_IsStable is a tripwire, not a specification. The
// fingerprint is persisted in the ledger for 90 days, so the same payload must
// hash the same way across every build in that window. If a Go release changes
// what json.Marshal emits, this fails on the upgrade - before in-flight
// resubmits start drawing 409s.
func TestNewPayloadFingerprint_IsStable(t *testing.T) {
	fp, err := NewPayloadFingerprint(json.RawMessage(`{"b":[1,{"y":"<&>","x":2.50}],"a":"é"}`))
	require.NoError(t, err)
	assert.Equal(t, "e64e9f7cbb23033b73265cd8bced5607", hex.EncodeToString(fp[:]))
}
