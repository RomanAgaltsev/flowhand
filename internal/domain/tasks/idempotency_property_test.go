package tasks

import (
	"bytes"
	"encoding/json"
	"maps"
	"math/rand/v2"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/stretchr/testify/require"
)

// objectInOrder writes obj as a JSON object with its keys in the given order.
// Building the text by hand is what makes the reordering real: json.Marshal
// would sort the keys and hand every property the same bytes.
func objectInOrder(t *testing.T, obj map[string]string, keys []string) json.RawMessage {
	t.Helper()
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		require.NoError(t, err)
		vb, err := json.Marshal(obj[k])
		require.NoError(t, err)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// shuffled returns obj's keys in a random order.
func shuffled(obj map[string]string) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	//nolint:gosec // G404: a test-only shuffle; nothing here is secret.
	rand.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	return keys
}

func TestPayloadFingerprintProperties(t *testing.T) {
	params := gopter.DefaultTestParameters()
	params.MinSuccessfulTests = 10000
	props := gopter.NewProperties(params)

	// Identifiers for keys, arbitrary strings for values: the awkward input
	// belongs in the values, where it tests escaping, not in the keys, where it
	// would make the shrinker report generator noise.
	objGen := gen.MapOf(gen.Identifier(), gen.AnyString())

	props.Property("key order does not change the fingerprint", prop.ForAll(
		func(obj map[string]string) bool {
			a, errA := NewPayloadFingerprint(objectInOrder(t, obj, shuffled(obj)))
			b, errB := NewPayloadFingerprint(objectInOrder(t, obj, shuffled(obj)))
			return errA == nil && errB == nil && a == b
		}, objGen,
	))

	props.Property("changing a value DOES change the fingerprint", prop.ForAll(
		func(obj map[string]string) bool {
			if len(obj) == 0 {
				return true // no value to change
			}
			before, err := NewPayloadFingerprint(objectInOrder(t, obj, shuffled(obj)))
			if err != nil {
				return false
			}
			mutated := maps.Clone(obj)
			for k := range mutated {
				mutated[k] += "x" // exactly one value
				break
			}
			after, err := NewPayloadFingerprint(objectInOrder(t, mutated, shuffled(mutated)))
			return err == nil && before != after
		}, objGen,
	))

	props.TestingRun(t)
}
