package obs_test

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/obs"
)

func TestHistogramView_AppliesSLOBuckets(t *testing.T) {
	// Env is required: newResource rejects an unknown environment rather than
	// silently emitting a resource with no deployment.environment.name.
	cfg := &config.Config{Env: "dev", Obs: config.Obs{ServiceName: "test"}}

	reg := prometheus.NewRegistry()
	mp, err := obs.NewMeterProvider(cfg, reg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })

	h, err := mp.Meter("test").Float64Histogram("widget.duration", metric.WithUnit("ms"))
	require.NoError(t, err)
	h.Record(context.Background(), 42)

	// The assertion that matters: our boundaries, not prometheus.DefBuckets.
	require.Equal(t,
		[]float64{5, 10, 20, 30, 40, 50, 75, 100, 250, 500, 1000},
		gatherBuckets(t, reg, "widget_duration"),
		"ms View did not apply — check the instrument-name/unit selector")
}

// TestHistogramView_SecondsInstrumentGetsSecondBuckets is the half that guards
// the regression the ms-only View invited: an instrument reporting seconds
// (stable HTTP semconv uses "s" for http.server.request.duration) must get the
// second-scaled boundaries, never the millisecond ones. Under a unit-blind
// selector this test fails with every observation landing in the first bucket,
// which in production reads as a permanently-met p99 SLO.
func TestHistogramView_SecondsInstrumentGetsSecondBuckets(t *testing.T) {
	cfg := &config.Config{Env: "dev", Obs: config.Obs{ServiceName: "test"}}

	reg := prometheus.NewRegistry()
	mp, err := obs.NewMeterProvider(cfg, reg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })

	h, err := mp.Meter("test").Float64Histogram("gadget.duration", metric.WithUnit("s"))
	require.NoError(t, err)
	h.Record(context.Background(), 0.042)

	require.Equal(t,
		[]float64{.005, .010, .020, .030, .040, .050, .075, .100, .250, .500, 1},
		gatherBuckets(t, reg, "gadget_duration"),
		"s View did not apply — a unit-blind selector would have used the ms boundaries")
}

// gatherBuckets returns the upper bounds of the first histogram whose name
// starts with prefix. The exporter appends a unit suffix, so match on prefix.
func gatherBuckets(t *testing.T, reg *prometheus.Registry, prefix string) []float64 {
	t.Helper()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), prefix) {
			continue
		}
		var bounds []float64
		for _, b := range mf.GetMetric()[0].GetHistogram().GetBucket() {
			bounds = append(bounds, b.GetUpperBound())
		}
		return bounds
	}

	t.Fatalf("no histogram gathered with prefix %q — instrument never reached the registry", prefix)
	return nil
}
