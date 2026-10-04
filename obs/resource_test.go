package obs_test

import (
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/justabill-org/justabill/obs"
)

func attrs(res *resource.Resource) map[string]string {
	out := map[string]string{}
	for _, kv := range res.Attributes() {
		out[string(kv.Key)] = kv.Value.String()
	}

	return out
}

func TestResourceAttributes(t *testing.T) {
	clearEnv(t)

	res, err := obs.NewResource(t.Context(), obs.Config{
		Service: "justabill-api", Version: "abc123", Environment: obs.EnvProduction,
	})
	if err != nil {
		t.Fatalf("NewResource: %v", err)
	}

	got := attrs(res)
	want := map[string]string{
		"service.name":                "justabill-api",
		"service.namespace":           "justabill",
		"service.version":             "abc123",
		"deployment.environment.name": "production",
		"telemetry.sdk.language":      "go",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	if got["service.instance.id"] == "" {
		t.Error("service.instance.id is empty")
	}
}

func TestResourceDefaultsAndEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("OTEL_SERVICE_NAME", "renamed")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=preview,service.version=deadbeef")

	res, err := obs.NewResource(t.Context(), obs.Config{Service: "justabill-api"})
	if err != nil {
		t.Fatalf("NewResource: %v", err)
	}

	got := attrs(res)
	for k, v := range map[string]string{
		"service.name": "renamed", "deployment.environment.name": "preview", "service.version": "deadbeef",
	} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (from the environment)", k, got[k], v)
		}
	}
}

func TestDefaultEnvironmentAndDetectors(t *testing.T) {
	clearEnv(t)

	res, err := obs.NewResource(t.Context(), obs.Config{Service: "justabill-api"})
	if err != nil {
		t.Fatal(err)
	}

	if got := attrs(res)["deployment.environment.name"]; got != obs.EnvDevelopment {
		t.Errorf("default environment = %q, want development", got)
	}

	if n := obs.DetectorCount(obs.Config{Service: "justabill-api"}); n != 1 {
		t.Errorf("Start runs %d detectors, want the GCP detector only", n)
	}
}

func TestDefaultSampler(t *testing.T) {
	clearEnv(t)

	env := func(name string) *resource.Resource {
		return resource.NewSchemaless(attribute.String("deployment.environment.name", name))
	}

	tests := []struct{ env, want string }{
		{obs.EnvProduction, "ParentBased{root:TraceIDRatioBased{0.1}"},
		{obs.EnvPreview, "ParentBased{root:TraceIDRatioBased{1}"},
		{obs.EnvDevelopment, "ParentBased{root:TraceIDRatioBased{1}"},
	}
	for _, tt := range tests {
		s := obs.DefaultSampler(env(tt.env))
		if s == nil || !strings.HasPrefix(s.Description(), tt.want) {
			t.Errorf("%s sampler = %v, want prefix %s", tt.env, s, tt.want)
		}
	}

	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")

	if s := obs.DefaultSampler(env(obs.EnvProduction)); s != nil {
		t.Errorf("with OTEL_TRACES_SAMPLER set, DefaultSampler = %v, want nil (the SDK reads it)", s.Description())
	}
}

func TestProtocol(t *testing.T) {
	clearEnv(t)

	check := func(signal, want string) {
		t.Helper()

		got, err := obs.Protocol(signal)
		if err != nil || got != want {
			t.Errorf("Protocol(%s) = %q, %v; want %q", signal, got, err, want)
		}
	}

	check("TRACES", "http/protobuf")

	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	check("TRACES", "grpc")

	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "http/protobuf")
	check("LOGS", "http/protobuf")
	check("METRICS", "grpc")
}
