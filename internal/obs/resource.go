package obs

import (
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

func newResource(cfg *config.Config) (*resource.Resource, error) {
	var env attribute.KeyValue
	switch cfg.Env {
	case "dev":
		env = semconv.DeploymentEnvironmentNameDevelopment
	case "staging":
		env = semconv.DeploymentEnvironmentNameStaging
	case "prod":
		env = semconv.DeploymentEnvironmentNameProduction
	default:
		// Failing loudly beats an unfilterable resource: the SDK drops the
		// zero-value KeyValue silently, so this is the only place it can surface.
		return nil, fmt.Errorf("unknown env %q (want dev|staging|prod)", cfg.Env)
	}

	return resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.Obs.ServiceName),
		semconv.ServiceVersion(cfg.Version),
		env,
	))
}
