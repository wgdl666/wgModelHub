package config

import (
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
	"gopkg.in/yaml.v3"
)

func gatewayVisionTestConfig() Config {
	cfg := validConfig()
	cfg.Providers["gateway"] = ProviderConfig{Models: []string{models.VMindDreamV1}, VMindGateway: &VMindGatewayProviderConfig{BaseURL: "http://gateway.example", Token: "fixture", PollInterval: 1, ResultStorage: ObjectStorageConfig{Provider: "s3", Region: "us-east-2", Bucket: "fixture"}}}
	return cfg
}

func TestVisionRequiresGatewayAndRejectsDirectSettings(t *testing.T) {
	for _, kind := range []string{"human_parser", "human_yolo", "facesim", "lens_distortion_correction", "segment_person"} {
		t.Run(kind, func(t *testing.T) {
			input := kind + ": {}\n"
			if kind == "segment_person" {
				input = kind + ": {method: bria}\n"
			}
			var p ProviderConfig
			if err := yaml.Unmarshal([]byte(input), &p); err != nil {
				t.Fatal(err)
			}
			if _, _, err := p.ResolveVisionGateway(nil); err == nil {
				t.Fatal("native direct provider accepted")
			}
			cfg := gatewayVisionTestConfig()
			p.VisionGateway = "gateway"
			resolved, target, err := p.ResolveVisionGateway(cfg.Providers)
			if err != nil || target == nil || resolved.VisionGatewayURL != "http://gateway.example" {
				t.Fatalf("resolve: %+v %v", resolved, err)
			}
			if p.VisionGatewayURL != "" {
				t.Fatal("resolution mutated configuration")
			}
			p.VisionGateway = "missing"
			if _, _, err := p.ResolveVisionGateway(cfg.Providers); err == nil {
				t.Fatal("missing target accepted")
			}
			for _, field := range []string{"base_url", "username", "password"} {
				decoder := yaml.NewDecoder(strings.NewReader("vision_gateway: gateway\n" + kind + ": {" + field + ": removed}\n"))
				if err := decoder.Decode(&p); err == nil {
					t.Fatalf("legacy %s accepted", field)
				}
			}
		})
	}
}
