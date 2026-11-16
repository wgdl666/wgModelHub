package config

import (
	"fmt"
	"net/url"
	"strings"
)

// ResolveVisionGateway 给启动时的客户端和渠道指标同一份有效地址。保留原配置副本，
// 显式引用的视觉适配器直接使用 Gateway 根地址构造统一请求。
func (p ProviderConfig) ResolveVisionGateway(providers map[string]ProviderConfig) (ProviderConfig, *VMindGatewayProviderConfig, error) {
	if p.VisionGateway == "" {
		return p, nil, nil
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

	// Gateway 鉴权只用现有 API token，
	// 因此有效配置不携带直连供应商的 Basic Auth。
	switch {
	case p.HumanParser != nil:
		p.HumanParser = &HumanParserProviderConfig{BaseURL: base}
	case p.HumanYOLO != nil:
		p.HumanYOLO = &HumanYOLOProviderConfig{BaseURL: base}
	case p.SegmentPerson != nil:
		segment := *p.SegmentPerson
		segment.BaseURL = base
		segment.Username, segment.Password = "", ""
		p.SegmentPerson = &segment
	case p.FaceSim != nil:
		// 人脸比对同样经过统一登记派发，禁止沿用旧 Basic Auth 直连。
		p.FaceSim = &FaceSimProviderConfig{BaseURL: base}
	case p.LensCorrection != nil:
		p.LensCorrection = &LensCorrectionProviderConfig{BaseURL: base}
	default:
		return p, nil, fmt.Errorf("vision_gateway is only supported by human_parser, human_yolo, facesim, segment_person and lens_distortion_correction")
	}
	return p, gateway, nil
}
