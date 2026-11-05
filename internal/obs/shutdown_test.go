package obs_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/obs"
)

// TestInit_ServerCanStart is named for what it protects. Init is the first thing
// `flowhand server` calls, so a red run here means the binary exits at startup,
// not that some metric is off. The semconv import in resource.go going out of
// step with the OTel SDK is how that happened once, and it was read as "the obs
// tests fail" for ten days.
func TestInit_ServerCanStart(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	// Nothing listens here, and nothing has to: the exporter connects lazily.
	cfg := &config.Config{Env: "dev", Obs: config.Obs{
		ServiceName: "test", LogLevel: "info", LogFormat: "json", OTLPEndpoint: "127.0.0.1:1",
	}}

	shutdown, reg, err := obs.Init(t.Context(), cfg)
	require.NoError(t, err, "flowhand server cannot start")
	require.NotNil(t, reg)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
}
