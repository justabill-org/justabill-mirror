package obs

import (
	"context"

	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// WithoutDetectors skips the GCP detector, which asks the metadata server.
func WithoutDetectors(cfg Config) Config {
	cfg.detectors = []resource.Detector{}
	return cfg
}

// DetectorCount is how many resource detectors Start would run for cfg.
func DetectorCount(cfg Config) int {
	return len(withDefaults(cfg).detectors)
}

// NewResource builds the resource Start would use.
func NewResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	return newResource(ctx, withDefaults(WithoutDetectors(cfg)))
}

// DefaultSampler is the sampler Start installs for res, or nil.
func DefaultSampler(res *resource.Resource) sdktrace.Sampler {
	return defaultSampler(res)
}

// Protocol is the OTLP protocol for a signal (TRACES, METRICS or LOGS).
func Protocol(signal string) (string, error) {
	return protocol(signal)
}
