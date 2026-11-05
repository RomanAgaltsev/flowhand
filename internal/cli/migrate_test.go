//go:build integration

package cli

import (
	"sync"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/storage"
	"github.com/RomanAgaltsev/flowhand/internal/testutil/pgtest"
	"github.com/RomanAgaltsev/flowhand/migrations"
)

// TestConcurrentMigrators proves the session-scoped advisory lock serializes two
// replicas booting at once. Remove goose.WithSessionLocker in
// storage.NewGooseProvider and this must FAIL with a duplicate-relation error
// from the loser — a gate you have never watched fail is a gate you have not
// tested.
func TestConcurrentMigrators(t *testing.T) {
	dsn := pgtest.EmptyDSN(t)
	ctx := t.Context()

	// A pool each, not one shared: two replicas are two processes, and the lock
	// is per backend connection.
	newMigrator := func() *goose.Provider {
		p, closeProvider, err := storage.NewGooseProvider(
			ctx,
			config.DB{DSN: dsn, MaxConns: 4, MinConns: 1},
			migrations.FS,
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, closeProvider()) })
		return p
	}

	// Bring the database to version 2 BEFORE racing — the state the lock actually
	// has to protect: two replicas boot, both read
	// goose_db_version at 2, both decide migration 3 is pending, both run it.
	//
	// Racing from an empty database instead proves nothing, and does so silently.
	// goose bootstraps its version table with a 1-SECOND constant backoff on
	// collision (provider_run.go, tryEnsureVersionTable). On a migration set this
	// small the winner finishes everything inside that sleep, so the loser wakes,
	// finds nothing pending, and the test passes with the locker removed.
	seed := newMigrator()
	_, err := seed.UpTo(ctx, 2)
	require.NoError(t, err, "seeding to version 2 must succeed")

	sources := seed.ListSources()
	require.GreaterOrEqual(t, len(sources), 3, "the race needs a migration after version 2")
	wantApplied := len(sources) - 2 // everything the seed left pending

	first, second := newMigrator(), newMigrator()

	// The barrier is what makes this a race rather than two sequential runs.
	var start sync.WaitGroup
	start.Add(1)

	var (
		mu      sync.Mutex
		applied int
	)

	g, gctx := errgroup.WithContext(ctx)
	for i, p := range []*goose.Provider{first, second} {
		g.Go(func() error {
			start.Wait()
			results, err := p.Up(gctx)
			t.Logf("migrator %d: applied %d, err=%v", i, len(results), err)
			if err != nil {
				return err
			}
			mu.Lock()
			applied += len(results)
			mu.Unlock()
			return nil
		})
	}
	start.Done()

	require.NoError(t, g.Wait(), "both migrators must succeed; the loser waits, it does not crash")

	// Exactly-once, without going near the database: the winner reports every
	// migration it applied, and the loser — which re-reads goose_db_version
	// after the lock frees — reports none. Both applying would double this.
	require.Equal(t, wantApplied, applied, "each migration must be applied exactly once")

	version, err := first.GetDBVersion(ctx)
	require.NoError(t, err)
	require.EqualValues(t, sources[len(sources)-1].Version, version,
		"the schema must end at the newest migration")
}
