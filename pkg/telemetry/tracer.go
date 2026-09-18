// Package telemetry 初始化 OpenTelemetry 链路追踪，三个服务共用。

package telemetry

import (
	"context"
	"runtime/debug"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	ServiceName  string
	OTLPEndpoint string
	SampleRatio  float64
}

func Setup(ctx context.Context, cfg Config) (*sdktrace.TracerProvider, error) {

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
		// 容器网络内明文传输；生产上 collector 通常在同一个内网，TLS 由
		// 基础设施层（service mesh / 网络策略）负责，应用不强求。
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	// resource 是随每个 span 附带的进程身份标签，Jaeger 里按 service 名
	// 分组/检索就靠它。
	res, err := sdkresource.New(ctx,
		sdkresource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			// semconv 版本号变量：让 span 里的 schema 语义可追溯。
			semconv.ServiceVersion(version()),
		),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(

		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),

		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)

	otel.SetTracerProvider(tp)

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	return tp, nil
}

func TraceIDFromContext(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return ""
}

func version() string {
	if bi, ok := debugBuildInfo(); ok && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func debugBuildInfo() (*debug.BuildInfo, bool) {
	return debug.ReadBuildInfo()
}
