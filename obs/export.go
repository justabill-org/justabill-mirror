package obs

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// OTLP protocols (OTEL_EXPORTER_OTLP_PROTOCOL). http/protobuf is the specification's default.
const (
	protoGRPC = "grpc"
	protoHTTP = "http/protobuf"
)

// Signal names as they appear in the OTEL_* variables.
const (
	sigTraces  = "TRACES"
	sigMetrics = "METRICS"
	sigLogs    = "LOGS"
)

// signals says which signals are exported over OTLP.
type signals struct {
	traces, metrics, logs bool
}

func (s signals) any() bool {
	return s.traces || s.metrics || s.logs
}

// enabledSignals reads the standard variables: OTEL_SDK_DISABLED=true turns everything off,
// OTEL_<SIGNAL>_EXPORTER=none turns one signal off, and a signal is on only when it has an
// endpoint (OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT). Requiring an
// endpoint, rather than defaulting to localhost as the SDK would, is what keeps obs inert.
func enabledSignals() (signals, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true") {
		return signals{}, nil
	}

	var (
		sigs signals
		err  error
	)

	if sigs.traces, err = signalEnabled(sigTraces); err != nil {
		return signals{}, err
	}

	if sigs.metrics, err = signalEnabled(sigMetrics); err != nil {
		return signals{}, err
	}

	if sigs.logs, err = signalEnabled(sigLogs); err != nil {
		return signals{}, err
	}

	return sigs, nil
}

func signalEnabled(signal string) (bool, error) {
	switch exporter := strings.TrimSpace(os.Getenv("OTEL_" + signal + "_EXPORTER")); exporter {
	case "none":
		return false, nil
	case "", "otlp":
		return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
			os.Getenv("OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT") != "", nil
	default:
		return false, fmt.Errorf("obs: OTEL_%s_EXPORTER=%q: only otlp and none are supported", signal, exporter)
	}
}

// protocol is OTEL_EXPORTER_OTLP_<SIGNAL>_PROTOCOL, then OTEL_EXPORTER_OTLP_PROTOCOL, then
// http/protobuf. The exporters read the endpoint, headers, timeout and compression themselves.
func protocol(signal string) (string, error) {
	p := os.Getenv("OTEL_EXPORTER_OTLP_" + signal + "_PROTOCOL")
	if p == "" {
		p = os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}

	switch p = strings.TrimSpace(p); p {
	case "", protoHTTP:
		return protoHTTP, nil
	case protoGRPC:
		return protoGRPC, nil
	default:
		return "", fmt.Errorf("obs: OTLP protocol %q for %s: only grpc and http/protobuf are supported",
			p, strings.ToLower(signal))
	}
}

func newSpanExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	p, err := protocol(sigTraces)
	if err != nil {
		return nil, err
	}

	if p == protoGRPC {
		return otlptracegrpc.New(ctx)
	}

	return otlptracehttp.New(ctx)
}

func newMetricExporter(ctx context.Context) (sdkmetric.Exporter, error) {
	p, err := protocol(sigMetrics)
	if err != nil {
		return nil, err
	}

	if p == protoGRPC {
		return otlpmetricgrpc.New(ctx)
	}

	return otlpmetrichttp.New(ctx)
}

func newLogExporter(ctx context.Context) (sdklog.Exporter, error) {
	p, err := protocol(sigLogs)
	if err != nil {
		return nil, err
	}

	if p == protoGRPC {
		return otlploggrpc.New(ctx)
	}

	return otlploghttp.New(ctx)
}
