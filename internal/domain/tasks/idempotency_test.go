package tasks

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalJSON(t *testing.T) {
	t.Parallel()

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
		// Integers keep their digits: as doubles, two ids differing in the last digits
		// would fingerprint identically.
		{"big integer survives", `{"n":12345678901234567890}`, `{"n":12345678901234567890}`},
		// A JavaScript client and a Go client spell the same number differently, and
		// that must not turn a legitimate retry into a 409.
		{"1.0 is 1", `{"n":1.0}`, `{"n":1}`},
		{"exponent spelling", `{"n":1e2}`, `{"n":100}`},
		{"string escapes normalized", `{"s":"\u0041"}`, `{"s":"A"}`},
		// The minimal form, which is not what encoding/json would write.
		{"html is not escaped", `{"h":"<"}`, `{"h":"<"}`},
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
	t.Parallel()

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
		// These two used to be accepted, and each made two different payloads one
		// request: a false 200 on a key reused with a different body.
		{"duplicate name", `{"a":1,"a":2}`},
		{"nested duplicate name", `{"a":{"b":1,"b":2}}`},
		{"invalid utf-8", "{\"s\":\"\xff\"}"},
		{"lone surrogate", `{"s":"\ud800"}`},
		// Valid JSON that Postgres JSONB refuses: each of these was a client-triggered 500.
		{"NUL in a value", `{"a":"\u0000"}`},
		{"NUL in a name", `{"\u0000":1}`},
		{"NUL deep inside", `{"a":[{"b":"x\u0000y"}]}`},
		{"number past numeric's weight", `{"n":1e999999}`},
		{"number past numeric's scale", `{"n":1e-16384}`},
		{"zero with a huge exponent", `{"n":0e999999}`},
		{"number past double range", `{"n":1e400}`},
		{"integer past double range", `{"n":1` + strings.Repeat("0", 400) + `}`},
		{"fraction past numeric's scale", `{"n":1.` + strings.Repeat("0", 20000) + `}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := canonicalJSON(json.RawMessage(tt.in))
			require.ErrorIs(t, err, ErrInvalidPayload)
		})
	}
}

func TestNewPayloadFingerprint(t *testing.T) {
	t.Parallel()

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
// what the canonical form looks like, this fails on the upgrade - before
// in-flight resubmits start drawing 409s.
func TestNewPayloadFingerprint_IsStable(t *testing.T) {
	t.Parallel()

	fp, err := NewPayloadFingerprint(json.RawMessage(`{"b":[1,{"y":"<&>","x":2.50}],"a":"é"}`))
	require.NoError(t, err)
	assert.Equal(t, "072b17ee1581da87c49cdf103502198e", hex.EncodeToString(fp[:]))
}

// TestCanonicalJSON_AcceptsTheBoundary pins the other side of the rejections
// above: a limit that also refuses ordinary input is a new outage, not a fix.
func TestCanonicalJSON_AcceptsTheBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{"largest double", `{"n":1.7976931348623157e308}`},
		{"three-digit negative exponent", `{"n":1e-999}`},
		{"literal backslash-u is not NUL", `{"s":"\\u0000"}`},
		{"long exact decimal", `{"n":0.` + strings.Repeat("1", 1000) + `}`},
		{"zero exponent padding", `{"n":1e0002}`},
		{"not an object", `[1]`},
		{"scalar", `"x"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := canonicalJSON(json.RawMessage(tt.in))
			require.NoError(t, err)
		})
	}
}

// Two payloads the old canonicalizer told apart or merged wrongly. Both rows are
// the reason it was replaced.
func TestNewPayloadFingerprint_NumberSpellings(t *testing.T) {
	t.Parallel()

	a, err := NewPayloadFingerprint(json.RawMessage(`{"n":1}`))
	require.NoError(t, err)
	b, err := NewPayloadFingerprint(json.RawMessage(`{"n":1.0}`))
	require.NoError(t, err)
	assert.Equal(t, a, b, "1 and 1.0 are one number")

	c, err := NewPayloadFingerprint(json.RawMessage(`{"n":12345678901234567890}`))
	require.NoError(t, err)
	d, err := NewPayloadFingerprint(json.RawMessage(`{"n":12345678901234567891}`))
	require.NoError(t, err)
	assert.NotEqual(t, c, d, "integers beyond 2^53 stay distinct")
}
