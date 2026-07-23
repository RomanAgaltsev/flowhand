package obs

import (
	"fmt"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

// NewMeterProvider builds an OTel meter provider that exports through the given
// Prometheus registerer and collects runtime metrics.
func NewMeterProvider(cfg *config.Config, reg prometheus.Registerer) (*metric.MeterProvider, error) {
	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, fmt.Errorf("prometheus exporter: %w", err)
	}

	var deploymentEnvironmentName attribute.KeyValue
	switch cfg.Env {
	case "dev":
		deploymentEnvironmentName = semconv.DeploymentEnvironmentNameDevelopment
	case "staging":
		deploymentEnvironmentName = semconv.DeploymentEnvironmentNameStaging
	case "prod":
		deploymentEnvironmentName = semconv.DeploymentEnvironmentNameProduction
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.Obs.ServiceName),
			semconv.ServiceVersion(cfg.Version),
			deploymentEnvironmentName,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("merge resource: %w", err)
	}

	view := metric.NewView(
		metric.Instrument{
			Name: "latency",
		},
		metric.Stream{
			Aggregation: metric.AggregationExplicitBucketHistogram{
				Boundaries: []float64{5, 10, 20, 30, 40, 50, 75, 100, 250, 500, 1000},
			},
		},
	)

	meterProvider := metric.NewMeterProvider(
		metric.WithReader(exporter),
		metric.WithResource(res),
		metric.WithView(view),
	)

	if err := runtime.Start(
		runtime.WithMeterProvider(meterProvider),
		runtime.WithMinimumReadMemStatsInterval(5*time.Second),
	); err != nil {
		return nil, fmt.Errorf("runtime metrics: %w", err)
	}

	return meterProvider, nil
}
