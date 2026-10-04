package obs

import (
	"context"
	"crypto/rand"
	"os"
	"runtime/debug"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Namespace is service.namespace for every Just a Bill process.
const Namespace = "justabill"

// Values of deployment.environment.name.
const (
	EnvProduction  = "production"
	EnvPreview     = "preview"
	EnvDevelopment = "development"
)

// Root sampling ratios: production keeps 10% of new traces, everything else keeps them all.
const (
	productionSampleRatio = 0.1
	defaultSampleRatio    = 1.0
)

// newResource merges, lowest precedence first: the attributes from cfg, the SDK's own, the
// cloud detectors', and OTEL_RESOURCE_ATTRIBUTES / OTEL_SERVICE_NAME. An error still comes with a
// usable resource.
func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	version := cfg.Version
	if version == "" {
		version = buildRevision()
	}

	return resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(
			semconv.ServiceName(cfg.Service),
			semconv.ServiceNamespace(Namespace),
			semconv.ServiceVersion(version),
			semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
			semconv.ServiceInstanceID(rand.Text()),
		),
		resource.WithTelemetrySDK(),
		resource.WithDetectors(cfg.detectors...),
		resource.WithFromEnv(),
	)
}

// buildRevision is the VCS revision Go stamped into the binary, or "unknown" (go run, tests).
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return s.Value
		}
	}

	return "unknown"
}

// defaultSampler is parent-based with a trace-ID ratio at the root: 10% in production, 100%
// elsewhere. It returns nil when OTEL_TRACES_SAMPLER is set, so the SDK's own reading of the
// standard variables wins.
func defaultSampler(res *resource.Resource) sdktrace.Sampler {
	if os.Getenv("OTEL_TRACES_SAMPLER") != "" {
		return nil
	}

	ratio := defaultSampleRatio
	if environment(res) == EnvProduction {
		ratio = productionSampleRatio
	}

	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

func environment(res *resource.Resource) string {
	v, _ := res.Set().Value(semconv.DeploymentEnvironmentNameKey)
	if v.Type() != attribute.STRING {
		return ""
	}

	return v.AsString()
}
