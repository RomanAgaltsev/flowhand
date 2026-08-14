package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/storage"
)

// testDSN is never dialled - pgxpool connects lazily and these tests only read
// back the resolved config. No password: there is nothing to authenticate to,
// and gosec's G101 is right to object to one in a literal.
const testDSN = "postgres://user@127.0.0.1:1/flowhand?sslmode=disable"

// TestNewPool_ZeroConfigKeepsPgxDefaults pins the defect the very first CI run
// of the integration suite surfaced. NewPool used to copy every config field
// over the defaults ParseConfig had just applied, so an unset MaxConnLifetime
// arrived as 0 - and pgxpool treats that as an absolute deadline of "now"
// (maxAgeTime = time.Now().Add(lifetime)), expiring every connection the
// instant it is created. Acquire destroys and retries maxConns+1 times, then
// fails with "too many failed attempts acquiring connection".
//
// Two deliberate choices here:
//
//   - No database. This runs under `task test` on every PR, not only under
//     -tags=integration behind a Docker daemon.
//   - It asserts the resolved *configuration*, never elapsed time. The bug is
//     invisible on Windows, where the clock granularity is coarse enough that
//     time.Now() twice in a row reads equal and the expiry check comes back
//     false; a timing-based test would pass locally and fail only in CI, which
//     is precisely how this got missed.
func TestNewPool_ZeroConfigKeepsPgxDefaults(t *testing.T) {
	// Exactly the config the integration suite passes - MaxConns and MinConns
	// set, lifetime left unset - so this isolates the field that actually broke
	// CI rather than tripping on an invalid MaxConns first. pgxpool connects
	// lazily, so nothing dials.
	pool, err := storage.NewPool(context.Background(), config.DB{
		DSN:      testDSN,
		MaxConns: 4,
		MinConns: 1,
	})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	assert.Greater(t, pool.Config().MaxConnLifetime, time.Duration(0),
		"a zero MaxConnLifetime expires every connection the moment it is created")
}

// TestNewPool_ExplicitConfigWins guards the other direction: the zero-guard
// must not swallow values the operator actually set.
func TestNewPool_ExplicitConfigWins(t *testing.T) {
	pool, err := storage.NewPool(context.Background(), config.DB{
		DSN:             testDSN,
		MaxConns:        7,
		MinConns:        3,
		MaxConnLifetime: 42 * time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	assert.Equal(t, int32(7), pool.Config().MaxConns)
	assert.Equal(t, int32(3), pool.Config().MinConns)
	assert.Equal(t, 42*time.Minute, pool.Config().MaxConnLifetime)
}
