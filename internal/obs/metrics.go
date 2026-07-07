package obs

import (
	"context"
	"net/http"
	"net/http/pprof"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewMeterProvider(ctx context.Context, cfg *config.Config) (*metric.MeterProvider, error) {
	meterExporter, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
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

	resource, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.Obs.ServiceName),
			semconv.ServiceVersion(cfg.Version),
			deploymentEnvironmentName,
		),
	)

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
		metric.WithReader(
			metric.NewPeriodicReader(meterExporter),
		),
		metric.WithResource(resource),
		metric.WithView(view),
	)

	err = runtime.Start(runtime.WithMinimumReadMemStatsInterval(5 * time.Second))
	if err != nil {
		return nil, err
	}

	return meterProvider, nil
}

func DebugHandler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/debug/pprof/", pprof.Index)

	return mux
}
