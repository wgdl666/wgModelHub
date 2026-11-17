package config

import (
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// ResolveVisionGateway 给启动时的客户端和渠道指标同一份有效地址。保留原配置副本，
// 显式引用的视觉适配器直接使用 Gateway 根地址构造统一请求。
func (p ProviderConfig) ResolveVisionGateway(providers map[string]ProviderConfig) (ProviderConfig, *VMindGatewayProviderConfig, error) {
	vision := p.HumanParser != nil || p.HumanYOLO != nil || p.FaceSim != nil || p.SegmentPerson != nil || p.LensCorrection != nil
	if p.VisionGateway == "" {
		if vision {
			return p, nil, fmt.Errorf("self-hosted vision provider requires vision_gateway")
		}
		return p, nil, nil
	}
	if !vision {
		return p, nil, fmt.Errorf("vision_gateway is only supported by human_parser, human_yolo, facesim, segment_person and lens_distortion_correction")
	}
	gateway := providers[strings.TrimSpace(p.VisionGateway)].VMindGateway
	if gateway == nil {
		return p, nil, fmt.Errorf("vision_gateway must reference a configured model_gateway")
	}
	base := strings.TrimRight(strings.TrimSpace(gateway.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return p, nil, fmt.Errorf("vision_gateway requires an HTTP(S) gateway URL without credentials, query, or fragment")
	}

	p.VisionGatewayURL = base
	return p, gateway, nil
}

// GatewayVisionConfig 只选择视觉适配器，原生连接参数不能被 YAML 静默丢弃。
type GatewayVisionConfig struct{}

func (*GatewayVisionConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode || len(node.Content) != 0 {
		return fmt.Errorf("vision adapter must be {}; configure vision_gateway instead of native connection settings")
	}
	return nil
}

func (c *SegmentPersonProviderConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("segment_person must contain method settings")
	}
	for i := 0; i < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "method", "person_method", "subject_method":
		default:
			return fmt.Errorf("segment_person field %s is unsupported; configure vision_gateway for the connection", node.Content[i].Value)
		}
	}
	type settings SegmentPersonProviderConfig
	return node.Decode((*settings)(c))
}
