// Package telemetry sets up OpenTelemetry traces and metrics. Exporters follow the
// standard OTEL_* environment variables, such as OTEL_EXPORTER_OTLP_ENDPOINT.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	serviceName         = "kuma-analytics"
	defaultSuccessRatio = 0.01
	metricInterval      = 15 * time.Second
)

// Setup installs the global tracer and meter providers. Without an OTLP endpoint
// nothing is exported, but spans still get real ids, so beats still carry a trace id.
// Call the returned function on shutdown to flush.
func Setup(ctx context.Context, log *slog.Logger) (func(context.Context) error, error) {
	ratio, err := successRatio()
	if err != nil {
		return nil, err
	}

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attribute.String("service.name", serviceFromEnv())))
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}
	if envRes, err := resource.New(ctx, resource.WithFromEnv()); err == nil {
		if merged, err := resource.Merge(res, envRes); err == nil {
			res = merged
		}
	}

	traceOptions := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	}
	meterOptions := []sdkmetric.Option{sdkmetric.WithResource(res)}

	if exportEnabled() {
		traceExporter, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("create trace exporter: %w", err)
		}
		traceOptions = append(traceOptions, sdktrace.WithSpanProcessor(
			NewOutcomeProcessor(sdktrace.NewBatchSpanProcessor(traceExporter), ratio, log)))

		metricExporter, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("create metric exporter: %w", err)
		}
		meterOptions = append(meterOptions, sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(metricInterval))))
	} else {
		log.Info("OTEL_EXPORTER_OTLP_ENDPOINT is not set, traces and metrics are not exported")
	}

	tracerProvider := sdktrace.NewTracerProvider(traceOptions...)
	meterProvider := sdkmetric.NewMeterProvider(meterOptions...)
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
		return nil, fmt.Errorf("start runtime metrics: %w", err)
	}

	return func(ctx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
	}, nil
}

func exportEnabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

func serviceFromEnv() string {
	if name := os.Getenv("OTEL_SERVICE_NAME"); name != "" {
		return name
	}
	return serviceName
}

func successRatio() (float64, error) {
	raw := os.Getenv("KUMA_TRACE_SUCCESS_RATIO")
	if raw == "" {
		return defaultSuccessRatio, nil
	}
	ratio, err := strconv.ParseFloat(raw, 64)
	if err != nil || ratio < 0 || ratio > 1 {
		return 0, errors.New("KUMA_TRACE_SUCCESS_RATIO must be a number from 0 to 1")
	}
	return ratio, nil
}
