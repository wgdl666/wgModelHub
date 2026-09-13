package metricserver

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Reader 包装 OTel Prometheus exporter，作为 MeterProvider 唯一 Reader，供 /metrics 抓取。
type Reader struct {
	reader   *otelprom.Exporter
	registry *prometheus.Registry
}

// NewReader 创建独立 Registry；业务指标属性只按需带低基数标签，不把 resource 复制成 constant labels。
// 使用 exporter 默认 UnderscoreEscapingWithSuffixes；精确导出名由 contract test 锁定。
func NewReader() (*Reader, error) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(
		otelprom.WithRegisterer(registry),
		otelprom.WithoutScopeInfo(),
		otelprom.WithoutTargetInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("create prometheus metric reader: %w", err)
	}
	return &Reader{reader: exporter, registry: registry}, nil
}

func (r *Reader) ManualReader() sdkmetric.Reader {
	return r.reader
}

// Handler 暴露 Prometheus 文本抓取入口，挂到内部 HttpServer 的 /metrics。
func (r *Reader) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}
