package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

func TestSetupMetrics_RecordedMetricShowsUpInScrape(t *testing.T) {
	mp, handler, err := SetupMetrics()
	if err != nil {
		t.Fatalf("SetupMetrics: %v", err)
	}
	defer func() { _ = mp.Shutdown(t.Context()) }()

	counter, err := otel.Meter("test").Int64Counter("test.setupmetrics.calls")
	if err != nil {
		t.Fatalf("create counter: %v", err)
	}
	counter.Add(t.Context(), 42)

	body := scrape(t, handler)

	if !strings.Contains(body, "test_setupmetrics_calls_total{") || !strings.Contains(body, "} 42\n") {
		t.Fatalf("scraped output should contain the recorded counter = 42, got:\n%s", body)
	}
}

func TestSetupMetrics_RuntimeMetricsExposed(t *testing.T) {
	mp, handler, err := SetupMetrics()
	if err != nil {
		t.Fatalf("SetupMetrics: %v", err)
	}
	defer func() { _ = mp.Shutdown(t.Context()) }()

	scrape(t, handler)
	time.Sleep(50 * time.Millisecond)
	body := scrape(t, handler)

	if !strings.Contains(body, "go_goroutine_count") {
		t.Fatalf("scraped output should contain runtime metrics (go_goroutine_count), got:\n%s", body)
	}
}

// NewMetricsServer 只暴露 /metrics：运维端口上不该有业务路由，
// 其他路径一律 404——这条语义防止以后有人图省事往运维端口上挂业务接口。
func TestNewMetricsServer_OnlyMetricsPath(t *testing.T) {
	_, handler, err := SetupMetrics()
	if err != nil {
		t.Fatalf("SetupMetrics: %v", err)
	}
	srv := NewMetricsServer(handler)

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/products", nil))
	if rec.Code != 404 {
		t.Fatalf("non-metrics path status = %d, want 404", rec.Code)
	}
}

func scrape(t *testing.T, handler http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("scrape status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read scrape body: %v", err)
	}
	return string(body)
}
