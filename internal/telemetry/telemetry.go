package telemetry

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	resource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

func Init(ctx context.Context, service string) (func(context.Context) error, error) {
	ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if ep == "" {
		ep = "otel-collector:4317"
	}
	exp, e := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(ep), otlptracegrpc.WithInsecure())
	if e != nil {
		return nil, e
	}
	version := os.Getenv("APP_VERSION")
	if version == "" {
		version = "dev"
	}
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "local"
	}
	res, e := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(service), semconv.ServiceNamespace("distributed-request-debugger"), semconv.ServiceVersion(version), semconv.DeploymentEnvironmentName(env)))
	if e != nil {
		return nil, e
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp.Shutdown, nil
}

func InitMetrics(ctx context.Context, service string) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "otel-collector:4317"
	}
	exp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	version := os.Getenv("APP_VERSION")
	if version == "" {
		version = "dev"
	}
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "local"
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(service), semconv.ServiceNamespace("distributed-request-debugger"), semconv.ServiceVersion(version), semconv.DeploymentEnvironmentName(env)))
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(10*time.Second))), sdkmetric.WithResource(res))
	otel.SetMeterProvider(mp)
	return mp.Shutdown, nil
}
