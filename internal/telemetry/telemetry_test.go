package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// This microbenchmark compares one in-process request through a tiny handler
// with and without server instrumentation. It excludes network, exporter I/O,
// database work, and production load; use it only to inspect local overhead.
func BenchmarkHTTPHandlerInstrumentation(b *testing.B) {
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	defer provider.Shutdown(b.Context())
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	benchmarks := []struct {
		name    string
		handler http.Handler
	}{
		{"disabled", base},
		{"otelhttp", otelhttp.NewHandler(base, "benchmark.handler")},
	}
	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r := httptest.NewRequest(http.MethodGet, "/benchmark", nil)
				w := httptest.NewRecorder()
				bm.handler.ServeHTTP(w, r)
			}
		})
	}
}
