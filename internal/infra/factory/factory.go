package factory

import (
	"context"
	"fmt"
	"strings"

	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/infra/ark"
	"github.com/wgdl666/wgModelHub/internal/infra/genai"
	"github.com/wgdl666/wgModelHub/internal/infra/ltx"
	"github.com/wgdl666/wgModelHub/internal/infra/openai"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

// Build 按 Nacos providers 段实例化供应商能力；profile 路由在 service 层完成，这里只负责连接与能力装配。
func Build(ctx context.Context, cfg config.Config) (map[string]provider.Set, error) {
	sets := make(map[string]provider.Set, len(cfg.Providers))
	for name, providerCfg := range cfg.Providers {
		set, err := buildProvider(ctx, name, providerCfg)
		if err != nil {
			return nil, fmt.Errorf("provider %s: %w", name, err)
		}
		sets[name] = set
	}
	return sets, nil
}

func buildProvider(ctx context.Context, name string, providerCfg config.ProviderConfig) (provider.Set, error) {
	switch strings.ToLower(strings.TrimSpace(providerCfg.Type)) {
	case "gemini":
		client, err := genai.NewGemini(ctx, name, providerCfg.APIKey, providerCfg.BaseURL, providerCfg.ProxyURL)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client, Image: client}, nil
	case "vertexai":
		client, err := genai.NewVertexAI(ctx, name, providerCfg.Project, providerCfg.Location)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case "ark":
		client, err := ark.New(name, providerCfg.APIKey, providerCfg.BaseURL)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case "openai":
		client, err := openai.New(name, providerCfg.APIKey, providerCfg.BaseURL, providerCfg.SendEnableThinking)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case "ltx":
		client, err := ltx.New(
			name,
			providerCfg.BaseURL,
			providerCfg.Token,
			providerCfg.Duration,
			providerCfg.FPS,
			providerCfg.Seed,
			providerCfg.PollInterval,
			providerCfg.MaxPollTime,
		)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Video: client}, nil
	default:
		return provider.Set{}, provider.New(provider.ErrorConfiguration, fmt.Sprintf("provider %s type %q is unsupported", name, providerCfg.Type))
	}
}
