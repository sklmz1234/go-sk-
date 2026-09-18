// metrics.go：指标支柱的初始化，与 tracer.go 完全对称——同一个 OTel SDK，
// trace 走 push（OTLP 推给 Jaeger），metrics 走 pull（Prometheus 来 /metrics 拉）。

package telemetry

import (
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const MetricsPort = 9464

func SetupMetrics() (*sdkmetric.MeterProvider, http.Handler, error) {
	exp, err := prometheus.New()
	if err != nil {
		return nil, nil, err
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp))

	otel.SetMeterProvider(mp)

	if err := runtime.Start(); err != nil {
		return nil, nil, err
	}

	return mp, promhttp.Handler(), nil
}

func NewMetricsServer(handler http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", handler)
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", MetricsPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}
