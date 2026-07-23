package obs

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

// NewTracerProvider builds an OTel tracer provider that exports spans over OTLP/gRPC.
func NewTracerProvider(ctx context.Context, cfg *config.Config) (*trace.TracerProvider, error) {
	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.Obs.OTLPEndpoint),
		otlptracegrpc.WithInsecure(),
	)
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

	traceProvider := trace.NewTracerProvider(
		trace.WithBatcher(traceExporter,
			trace.WithBatchTimeout(time.Second),
		),
		trace.WithResource(res),
	)

	return traceProvider, nil
}
