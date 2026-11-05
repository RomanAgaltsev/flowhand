//go:build integration

package cli

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/testutil/pgtest"
)

// TestServerCommand_Starts runs `flowhand server` the way main does, from an empty
// config, and waits for it to answer. Everything else tests a piece of the wiring;
// this is the only test that notices the wiring as a whole does not start, which
// happened twice on one branch for two unrelated reasons. Each piece was green.
func TestServerCommand_Starts(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	addr := freeAddr(t)
	t.Setenv("FLOWHAND_DB_DSN", pgtest.EmptyDSN(t))
	t.Setenv("FLOWHAND_HTTP_ADDR", addr)
	t.Setenv("FLOWHAND_OBS_OTLP_ENDPOINT", "127.0.0.1:1") // connected to lazily

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	exited := make(chan error, 1)
	go func() {
		rootCmd.SetArgs([]string{"server"})
		exited <- rootCmd.ExecuteContext(ctx)
	}()

	client := &http.Client{Timeout: time.Second}
	t.Cleanup(client.CloseIdleConnections)

	deadline := time.After(30 * time.Second)
	for !answers(ctx, t, client, "http://"+addr+"/metrics") {
		select {
		case err := <-exited:
			require.Fail(t, "flowhand server exited at startup", "%v", err)
		case <-deadline:
			require.Fail(t, "flowhand server never answered")
		case <-time.After(50 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-exited:
		assert.NoError(t, err, "a canceled server must shut down cleanly")
	case <-time.After(30 * time.Second):
		require.Fail(t, "flowhand server did not shut down")
	}
}

// answers reports whether url serves a 200 yet.
func answers(ctx context.Context, t *testing.T, client *http.Client, url string) bool {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	require.NoError(t, err)

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { require.NoError(t, resp.Body.Close()) }()

	return resp.StatusCode == http.StatusOK
}

// freeAddr reserves a loopback port by binding and releasing it. A small race, and
// the alternative is a server that reports its own port back.
func freeAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { require.NoError(t, l.Close()) }()

	return l.Addr().String()
}
