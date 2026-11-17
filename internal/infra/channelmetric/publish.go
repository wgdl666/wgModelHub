package channelmetric

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/channel"
)

// Publisher 把当前配置的渠道写到已有的 Prometheus 抓取入口。
// 标签只有实例、渠道、主机和协议，不放凭据、完整 URL 或代理。
type Publisher struct {
	gauge *prometheus.GaugeVec
}

func New(registerer prometheus.Registerer) (*Publisher, error) {
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "wg_modelhub_channel_info",
		Help: "Derived model API channel for each configured instance. Value is 1. Excludes credentials, URL paths and proxies.",
	}, []string{"instance", "channel", "host", "protocol"})
	if err := registerer.Register(gauge); err != nil {
		return nil, fmt.Errorf("register channel info: %w", err)
	}
	return &Publisher{gauge: gauge}, nil
}

// Publish 用当前 providers 整表替换指标。热更新后旧实例名不再留在抓取结果里。
// New 成功后 gauge 已经注册，调用方拿到的就是这个对象。
func (p *Publisher) Publish(providers map[string]config.ProviderConfig) error {
	next := make([]prometheus.Labels, 0, len(providers))
	for name, provider := range providers {
		resolved, _, err := provider.ResolveVisionGateway(providers)
		if err != nil {
			return fmt.Errorf("provider %s: %w", name, err)
		}
		id, err := channel.Of(resolved)
		if err != nil {
			return fmt.Errorf("provider %s: %w", name, err)
		}
		next = append(next, prometheus.Labels{
			"instance": name,
			"channel":  id.Channel,
			"host":     id.Host,
			"protocol": id.Protocol,
		})
	}
	p.gauge.Reset()
	for _, labels := range next {
		p.gauge.With(labels).Set(1)
	}
	return nil
}
