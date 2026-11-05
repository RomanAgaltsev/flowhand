package tasks

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// propertyCases is how many generated cases a property runs: 10^4 normally, and a
// tenth of that under -short, which is what the -race gate uses. The detector
// makes these properties several times slower, and the suite has to stay inside
// go test's default timeout as properties are added.
func propertyCases() int {
	if testing.Short() {
		return 1_000
	}
	return 10_000
}

// fakeCatalog stands in for the worker's registry. The domain cannot import
// the real one — that would point the dependency arrow outward — so tests
// supply the port from the outside, exactly as the composition root will.
type fakeCatalog struct{ names map[string]bool }

func (f fakeCatalog) Has(name string) bool { return f.names[name] }

func newFakeCatalog(names ...string) fakeCatalog {
	f := fakeCatalog{names: make(map[string]bool, len(names))}
	for _, n := range names {
		f.names[n] = true
	}
	return f
}

// printableASCII generates strings of printable ASCII (0x20..0x7E). gopter
// v0.2.11 ships no ASCIIString generator, so assemble one from a rune range;
// string length follows the property's MinSize/MaxSize parameters.
func printableASCII() gopter.Gen {
	return gen.SliceOf(gen.RuneRange(0x20, 0x7E)).Map(func(rs []rune) string {
		return string(rs)
	})
}

// --- Unit: constructor boundaries -----------------------------------------
//
// The property tests below catch any bad boundary automatically; these named
// examples exist because a fixed failing case is far easier to debug than a
// shrunk counterexample.

func TestPriorityConstructs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      uint8
		want    Priority
		wantErr error
	}{
		{name: "zero is valid", in: 0, want: 0},
		{name: "MaxPriority is valid", in: 9, want: 9},
		{name: "one past MaxPriority", in: 10, wantErr: ErrInvalidPriority},
		{name: "255 is invalid", in: 255, wantErr: ErrInvalidPriority},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewPriority(tt.in)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Higher = sooner. The method exists so the call site never has to
// remember which way the numbers run.
func TestPriorityIsHigherThan(t *testing.T) {
	t.Parallel()

	assert.True(t, Priority(9).IsHigherThan(Priority(3)))
	assert.False(t, Priority(3).IsHigherThan(Priority(9)))
	assert.False(t, Priority(5).IsHigherThan(Priority(5))) // strict: a tie is not "higher"
	assert.True(t, Priority(1).IsHigherThan(Priority(0)))  // boundary at the bottom
	assert.False(t, Priority(0).IsHigherThan(Priority(1)))
}

func TestIdempotencyKeyConstructs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{name: "empty", in: "", wantErr: ErrInvalidIdempotencyKey},
		{name: "128 ASCII bytes", in: strings.Repeat("a", 128)},
		{name: "129 ASCII bytes", in: strings.Repeat("a", 129), wantErr: ErrIdempotencyKeyTooLong},
		// "café" is 4 runes but 5 bytes — the byte-vs-rune ambiguity the
		// ASCII check exists to settle. The check must run before length,
		// or a long non-ASCII string reports the wrong reason.
		{name: "multi-byte rune", in: "café", wantErr: ErrInvalidIdempotencyKey},
		{name: "high-bit byte (invalid UTF-8)", in: "\xff", wantErr: ErrInvalidIdempotencyKey},
		{name: "printable ASCII incl. punctuation", in: "order-123: pending!"},
		// Postgres refuses NUL in a TEXT column outright, and the wire pattern is the
		// only other thing in the way of a 500. The rest have no business in a log.
		{name: "NUL", in: "\x00", wantErr: ErrIdempotencyKeyNotPrintable},
		{name: "NUL inside", in: "k\x00", wantErr: ErrIdempotencyKeyNotPrintable},
		{name: "control character", in: "a\x1fb", wantErr: ErrIdempotencyKeyNotPrintable},
		{name: "DEL", in: "\x7f", wantErr: ErrIdempotencyKeyNotPrintable},
		{name: "space is printable", in: "a b"},
		{name: "tilde is the last printable byte", in: "~"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := NewIdempotencyKey(tt.in)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.in, k.String())
		})
	}
}

// A value object has no identity: two keys built from the same string must be
// interchangeable.
func TestIdempotencyKeyValueSemantics(t *testing.T) {
	t.Parallel()

	first, err := NewIdempotencyKey("idem-1")
	require.NoError(t, err)
	second, err := NewIdempotencyKey("idem-1")
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

// TestIdempotencyKeyLogValue pins why the method exists: the JSON handler marshals
// a struct with no exported fields as {}, which loses the one field an operator
// needs.
func TestIdempotencyKeyLogValue(t *testing.T) {
	t.Parallel()

	key, err := NewIdempotencyKey("idem-1")
	require.NoError(t, err)

	var out bytes.Buffer
	slog.New(slog.NewJSONHandler(&out, nil)).Info("msg", "idempotency_key", key)

	assert.Contains(t, out.String(), `"idempotency_key":"idem-1"`)
}

func TestHandlerConstructs(t *testing.T) {
	t.Parallel()

	cat := newFakeCatalog("a", "b", "c")

	for _, name := range []string{"a", "b", "c"} {
		h, err := NewHandler(name, cat)
		require.NoError(t, err, "catalog knows %q", name)
		assert.Equal(t, name, h.Name())
	}

	_, err := NewHandler("d", cat)
	assert.ErrorIs(t, err, ErrUnknownHandler)

	_, err = NewHandler("", cat)
	assert.ErrorIs(t, err, ErrEmptyHandler)

	// Checked before the catalog: a TEXT column refuses both, and a permissive
	// catalog must not be what lets them through to a 500.
	for _, name := range []string{"a\x00b", "\xff"} {
		_, err = NewHandler(name, newFakeCatalog(name))
		assert.ErrorIs(t, err, ErrInvalidHandlerName, "%q", name)
	}
}

// A Handler value is proof the name was registered at construction time —
// and only then. Validity is as-of-construction; the same string through the
// same catalog must yield interchangeable values.
func TestHandlerValueSemantics(t *testing.T) {
	t.Parallel()

	cat := newFakeCatalog("a")

	first, err := NewHandler("a", cat)
	require.NoError(t, err)
	second, err := NewHandler("a", cat)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestHandlerRejectsNilCatalog(t *testing.T) {
	t.Parallel()

	_, err := NewHandler("a", nil)
	assert.ErrorIs(t, err, ErrNilHandlerCatalog)
}

func TestLeaseEpochIsStaleVs(t *testing.T) {
	t.Parallel()

	const maxEpoch = LeaseEpoch(^uint64(0))
	tests := []struct {
		name  string
		e     LeaseEpoch
		other LeaseEpoch
		want  bool
	}{
		{name: "older than other", e: 1, other: 2, want: true},
		{name: "newer than other", e: 2, other: 1, want: false},
		{name: "equal is not stale", e: 7, other: 7, want: false},
		{name: "zero vs max", e: 0, other: maxEpoch, want: true},
		{name: "max vs zero", e: maxEpoch, other: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.e.IsStaleVs(tt.other))
		})
	}
}

func TestShardIDBucket(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    ShardID
		n    int
		want int
	}{
		{name: "zero lands in bucket zero", s: 0, n: 8, want: 0},
		{name: "last bucket", s: 7, n: 8, want: 7},
		{name: "wraps at n", s: 8, n: 8, want: 0},
		{name: "n-1 wraps to last", s: 15, n: 8, want: 7},
		{name: "top of the uint16 range", s: 65535, n: 8, want: 7},
		{name: "degenerate single bucket", s: 41, n: 1, want: 0},
		{name: "zero buckets is defined, not a panic", s: 41, n: 0, want: 0},
		{name: "negative buckets is defined", s: 41, n: -8, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.s.Bucket(tt.n))
		})
	}
}

// --- Properties (gopter) ---------------------------------------------------
//
// Each property states a rule over all inputs rather than six examples.
// Conditions return a string: empty means pass, non-empty becomes the
// counterexample text gopter reports next to the shrunk value.

func TestPropertiesPriority(t *testing.T) {
	t.Parallel()

	params := gopter.DefaultTestParameters()
	params.MinSuccessfulTests = propertyCases()
	properties := gopter.NewProperties(params)

	// A clean partition of the whole uint8 range: everything at or below the
	// published maximum constructs, everything above fails with the sentinel.
	// Strictly stronger than a table at 0/9/10 — it also covers 255, 128 and
	// every value nobody thought to write down.
	//
	// The boundary is deliberately the literal 9, NOT
	// MaxPriority: if the constant ever drifts from the published range, this
	// property is the tripwire that catches it (the break-one-on-purpose
	// check relies on exactly that).
	const publishedMax uint8 = 9
	properties.Property("NewPriority accepts exactly 0..9", prop.ForAll(
		func(p uint8) string {
			got, err := NewPriority(p)
			if p <= publishedMax {
				switch {
				case err != nil:
					return fmt.Sprintf("priority %d: unexpected error %v", p, err)
				case got != Priority(p):
					return fmt.Sprintf("priority %d: constructed %d", p, got)
				}
				return ""
			}
			switch {
			case err == nil:
				return fmt.Sprintf("priority %d: expected error, got none", p)
			case !errors.Is(err, ErrInvalidPriority):
				return fmt.Sprintf("priority %d: error %v does not wrap ErrInvalidPriority", p, err)
			}
			return ""
		},
		gen.UInt8(),
	))

	properties.TestingRun(t)
}

func TestPropertiesIdempotencyKey(t *testing.T) {
	t.Parallel()

	// Empty is a degenerate case with nothing to generate; pin it here so
	// the property file stands alone.
	if _, err := NewIdempotencyKey(""); err == nil || !errors.Is(err, ErrInvalidIdempotencyKey) {
		t.Errorf("empty key: want ErrInvalidIdempotencyKey, got %v", err)
	}

	params := gopter.DefaultTestParameters()
	params.MinSuccessfulTests = propertyCases()
	params.MinSize = 1 // string lengths 1..128 — MaxSize is exclusive
	params.MaxSize = MaxIdempotencyKeyBytes + 1
	properties := gopter.NewProperties(params)

	properties.Property("every non-empty printable ASCII string of at most 128 bytes constructs", prop.ForAll(
		func(s string) string {
			k, err := NewIdempotencyKey(s)
			switch {
			case err != nil:
				return fmt.Sprintf("%q: unexpected error %v", s, err)
			case k.String() != s:
				return fmt.Sprintf("%q: String() returned %q", s, k.String())
			}
			return ""
		},
		printableASCII(),
	))

	// Prefix and suffix stay ASCII, so the seed is the only possible
	// culprit. Seeds cover multi-byte runes and raw high-bit bytes (invalid
	// UTF-8): the domain check is byte-level, so both must fail.
	properties.Property("any string containing a non-ASCII byte fails as non-ASCII", prop.ForAll(
		func(prefix, seed, suffix string) string {
			s := prefix + seed + suffix
			_, err := NewIdempotencyKey(s)
			// Asserting the SPECIFIC sentinel is what makes this property honest:
			// prefix+suffix can exceed 128 bytes, so the umbrella sentinel alone
			// would also be satisfied by the length check firing instead.
			if !errors.Is(err, ErrIdempotencyKeyNotASCII) {
				return fmt.Sprintf("%q: want ErrIdempotencyKeyNotASCII, got %v", s, err)
			}
			return ""
		},
		printableASCII(),
		gen.OneConstOf("é", "ü", "ß", "Ж", "日", "🎉", "\x80", "\xff"),
		printableASCII(),
	))

	properties.Property("ASCII strings longer than 128 bytes fail", prop.ForAll(
		func(extra int) string {
			s := strings.Repeat("a", MaxIdempotencyKeyBytes+1+extra)
			_, err := NewIdempotencyKey(s)
			switch {
			case err == nil:
				return fmt.Sprintf("length %d: expected error, got none", len(s))
			case !errors.Is(err, ErrInvalidIdempotencyKey):
				return fmt.Sprintf("length %d: error %v does not wrap ErrInvalidIdempotencyKey", len(s), err)
			}
			return ""
		},
		gen.IntRange(0, 1000),
	))

	properties.TestingRun(t)
}

func TestPropertiesHandler(t *testing.T) {
	t.Parallel()

	params := gopter.DefaultTestParameters()
	params.MinSuccessfulTests = propertyCases()
	properties := gopter.NewProperties(params)

	// Has is the single source of truth: construction must agree with the
	// catalog on every input, known or not. The generator mixes catalog
	// names with arbitrary strings (including empty) so both sides of the
	// partition get exercised.
	cat := newFakeCatalog("a", "b", "c")

	properties.Property("constructs exactly the names the catalog knows", prop.ForAll(
		func(name string) string {
			_, err := NewHandler(name, cat)
			if cat.Has(name) {
				if err != nil {
					return fmt.Sprintf("%q is registered: unexpected error %v", name, err)
				}
				return ""
			}
			switch {
			case err == nil:
				return fmt.Sprintf("%q is not registered: expected error, got none", name)
			case !errors.Is(err, ErrUnknownHandler) && !errors.Is(err, ErrEmptyHandler):
				return fmt.Sprintf("%q: error %v wraps neither ErrUnknownHandler nor ErrEmptyHandler", name, err)
			}
			return ""
		},
		gen.OneGenOf(
			gen.OneConstOf("a", "b", "c"),
			printableASCII(),
		),
	))

	properties.TestingRun(t)
}

func TestPropertiesLeaseEpochIsStaleVs(t *testing.T) {
	t.Parallel()

	params := gopter.DefaultTestParameters()
	params.MinSuccessfulTests = propertyCases()
	properties := gopter.NewProperties(params)

	// The fencing test must order any two distinct epochs and never mark an
	// epoch stale against itself — anti-symmetry plus irreflexivity.
	properties.Property("IsStaleVs is anti-symmetric on distinct values and false on equal", prop.ForAll(
		func(a, b uint64) string {
			x, y := LeaseEpoch(a), LeaseEpoch(b)
			switch {
			case a == b:
				if x.IsStaleVs(y) || y.IsStaleVs(x) {
					return fmt.Sprintf("epoch %d: equal epochs must not be stale", a)
				}
			case a < b:
				if !x.IsStaleVs(y) || y.IsStaleVs(x) {
					return fmt.Sprintf("(%d, %d): older must be stale, newer must not", a, b)
				}
			default:
				if !y.IsStaleVs(x) || x.IsStaleVs(y) {
					return fmt.Sprintf("(%d, %d): older must be stale, newer must not", b, a)
				}
			}
			return ""
		},
		gen.UInt64(), gen.UInt64(),
	))

	properties.TestingRun(t)
}

// The one property here that can fail for an honest reason: 10^4 samples
// over 8 buckets has real variance, and ±20% is a judgement call rather than
// a law. The seed is fixed so a failure reproduces; if this ever flakes, raise
// samples rather than widening the tolerance — a wider tolerance stops the
// test detecting an actually-skewed mapping.
func TestPropertiesShardIDBucketDistribution(t *testing.T) {
	t.Parallel()

	const (
		buckets = 8
		samples = 10_000 // fixed, not propertyCases: the tolerance below is sized for it
	)
	expected := samples / buckets    // 1250
	delta := float64(expected) * 0.2 // ±20%

	counts := make([]int, buckets)
	//nolint:gosec // G404: fixed seed on purpose — reproducible failures,
	// no security relevance in a distribution test.
	rng := rand.New(rand.NewSource(1))
	for range samples {
		//nolint:gosec // G115: Intn(65536) < 65536 by construction.
		s := ShardID(rng.Intn(65536))
		b := s.Bucket(buckets)
		require.GreaterOrEqual(t, b, 0)
		require.Less(t, b, buckets)
		counts[b]++
	}

	for bucket, got := range counts {
		assert.InDelta(t, expected, got, delta,
			"bucket %d: %d of %d samples is outside ±20%% of %d", bucket, got, samples, expected)
	}
}
