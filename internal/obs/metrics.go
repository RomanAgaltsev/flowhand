package obs

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

// NewMeterProvider builds an OTel meter provider that exports through the given
// Prometheus registerer and collects runtime metrics.
func NewMeterProvider(cfg *config.Config, reg prometheus.Registerer) (*metric.MeterProvider, error) {
	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, fmt.Errorf("prometheus exporter: %w", err)
	}

	res, err := newResource(cfg)
	if err != nil {
		return nil, fmt.Errorf("merge resource: %w", err)
	}

	// Boundaries are milliseconds, so the selector pins Unit too: if an instrument
	// ever reports seconds (stable HTTP semconv uses "s" for
	// http.server.request.duration), this View must NOT match it. A View that
	// silently reinterprets seconds as milliseconds makes every SLO look met.
	msView := metric.NewView(
		metric.Instrument{Name: "*.duration", Unit: "ms"},
		metric.Stream{Aggregation: metric.AggregationExplicitBucketHistogram{
			Boundaries: []float64{5, 10, 20, 30, 40, 50, 75, 100, 250, 500, 1000},
		}},
	)

	// Same SLO, expressed in seconds, for instruments that follow stable semconv.
	sView := metric.NewView(
		metric.Instrument{Name: "*.duration", Unit: "s"},
		metric.Stream{Aggregation: metric.AggregationExplicitBucketHistogram{
			Boundaries: []float64{.005, .010, .020, .030, .040, .050, .075, .100, .250, .500, 1},
		}},
	)

	meterProvider := metric.NewMeterProvider(
		metric.WithReader(exporter),
		metric.WithResource(res),
		metric.WithView(msView, sView),
	)

	if err := runtime.Start(
		runtime.WithMeterProvider(meterProvider),
		runtime.WithMinimumReadMemStatsInterval(5*time.Second),
	); err != nil {
		return nil, fmt.Errorf("runtime metrics: %w", err)
	}

	return meterProvider, nil
}
