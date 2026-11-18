package config

import (
	"fmt"
	"net/url"
	"strings"
)

// ResolveVisionGateway 解析视觉适配器引用的网关，并将网关地址用于渠道识别。
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
		p.FaceSim = &FaceSimProviderConfig{BaseURL: base}
	case p.LensCorrection != nil:
		p.LensCorrection = &LensCorrectionProviderConfig{BaseURL: base}
	default:
		return p, nil, fmt.Errorf("vision_gateway is only supported by human_parser, human_yolo, facesim, segment_person and lens_distortion_correction")
	}
	return p, gateway, nil
}
