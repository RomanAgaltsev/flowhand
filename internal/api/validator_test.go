package api

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
)

// maxHandlerLen mirrors the maxLength constraint on CreateTaskRequest.handler
// in the OpenAPI spec. Keep in sync with internal/api/oas/oas_validators_gen.go.
const maxHandlerLen = 128

// FuzzCreateTaskRequest fuzzes the full untrusted-input path for a create-task
// body: ogen's generated JSON decoder followed by its generated validator.
//
// The corpus is raw request bodies rather than constructed structs, both
// because testing.F only accepts primitive seed types and because decoding is
// half of what we want under test.
func FuzzCreateTaskRequest(f *testing.F) {
	seeds := []string{
		// Well-formed.
		`{"handler":"echo"}`,
		`{"handler":"echo","payload":{"msg":"hello","n":42}}`,
		`{"handler":"echo","idempotency_key":"1f0a0c5e-3f2b-4a1d-9c7e-8b5a2d6f4e10"}`,
		`{"handler":"echo","payload":{"nested":{"a":[1,2,{"b":null}]}},"idempotency_key":"k-1"}`,

		// Structurally broken.
		``,
		`{`,
		`null`,
		`[]`,
		`"echo"`,
		`{"handler":123}`,

		// Required field missing or empty.
		`{}`,
		`{"handler":""}`,
		`{"payload":{"a":1}}`,

		// Length boundaries, counted in bytes.
		`{"handler":"` + strings.Repeat("a", maxHandlerLen) + `"}`,
		`{"handler":"` + strings.Repeat("a", maxHandlerLen+1) + `"}`,

		// Length boundaries, counted in runes: 128 runes but 384 bytes. The
		// validator counts runes, so this is valid and any byte-oriented
		// consumer downstream (column widths, buffers) has to cope.
		`{"handler":"` + strings.Repeat("日", maxHandlerLen) + `"}`,
		`{"handler":"` + strings.Repeat("日", maxHandlerLen+1) + `"}`,

		// Unicode edge cases: invalid UTF-8, escaped surrogate, NUL,
		// zero-width joiner, right-to-left override.
		"{\"handler\":\"\xff\xfe\"}",
		`{"handler":"😀"}`,
		`{"handler":"\ud800"}`,
		`{"handler":"a b"}`,
		`{"handler":"a‍b"}`,
		`{"handler":"‮echo"}`,

		// Optional-field tri-state: unset vs null vs empty vs wrong type.
		`{"handler":"echo","payload":null}`,
		`{"handler":"echo","payload":{}}`,
		`{"handler":"echo","payload":[]}`,
		`{"handler":"echo","idempotency_key":null}`,
		`{"handler":"echo","idempotency_key":""}`,

		// Duplicate keys: last-wins, first-wins and reject are all plausible.
		`{"handler":"echo","handler":"` + strings.Repeat("a", maxHandlerLen+1) + `"}`,

		// Numeric and nesting stress inside the opaque payload.
		`{"handler":"echo","payload":{"big":1e309,"neg":-0,"huge":123456789012345678901234567890}}`,
		`{"handler":"echo","payload":{"deep":` + strings.Repeat(`[`, 64) + strings.Repeat(`]`, 64) + `}}`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		var req oas.CreateTaskRequest
		if err := req.UnmarshalJSON([]byte(body)); err != nil {
			// Malformed input is an expected outcome, not a finding.
			return
		}
		if err := req.Validate(); err != nil {
			return
		}

		// Past this point the request is one the server would accept, so the
		// spec's constraints have to actually hold.
		if n := utf8.RuneCountInString(req.Handler); n < 1 || n > maxHandlerLen {
			t.Fatalf("validated request has out-of-range handler: %d runes, body %q", n, body)
		}
		if key, ok := req.IdempotencyKey.Get(); ok {
			if n := utf8.RuneCountInString(key); n < 1 || n > maxHandlerLen {
				t.Fatalf("validated request has out-of-range idempotency_key: %d runes, body %q", n, body)
			}
		}

		// Re-encoding an accepted request must produce something that decodes
		// back to the same value and is still accepted. Skip inputs carrying
		// invalid UTF-8: the encoder is entitled to replace those bytes, so a
		// mismatch there says nothing about the round-trip itself.
		if !utf8.ValidString(req.Handler) {
			return
		}

		encoded, err := req.MarshalJSON()
		if err != nil {
			t.Fatalf("encoding a validated request failed: %v, body %q", err, body)
		}

		var round oas.CreateTaskRequest
		if err := round.UnmarshalJSON(encoded); err != nil {
			t.Fatalf("re-decoding a validated request failed: %v, encoded %q", err, encoded)
		}
		if err := round.Validate(); err != nil {
			t.Fatalf("re-decoded request no longer validates: %v, encoded %q", err, encoded)
		}
		if round.Handler != req.Handler {
			t.Fatalf("handler changed across round-trip: %q -> %q", req.Handler, round.Handler)
		}
		if round.IdempotencyKey != req.IdempotencyKey {
			t.Fatalf("idempotency_key changed across round-trip: %+v -> %+v", req.IdempotencyKey, round.IdempotencyKey)
		}
		if got, want := len(round.Payload.Value), len(req.Payload.Value); got != want {
			t.Fatalf("payload size changed across round-trip: %d -> %d, encoded %q", want, got, encoded)
		}
	})
}
