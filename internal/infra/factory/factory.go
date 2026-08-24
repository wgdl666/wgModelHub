package factory

import (
	"context"
	"fmt"

	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/infra/ark"
	"github.com/wgdl666/wgModelHub/internal/infra/arkvideo"
	"github.com/wgdl666/wgModelHub/internal/infra/dashscopevideo"
	"github.com/wgdl666/wgModelHub/internal/infra/geminivideo"
	"github.com/wgdl666/wgModelHub/internal/infra/genai"
	"github.com/wgdl666/wgModelHub/internal/infra/ltx"
	"github.com/wgdl666/wgModelHub/internal/infra/ominilinkvideo"
	"github.com/wgdl666/wgModelHub/internal/infra/openai"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

// Build 按 Nacos providers 段实例化供应商能力；真实模型路由在 service 层完成，这里只负责连接与能力装配。
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
	switch {
	case providerCfg.Gemini != nil:
		cfg := providerCfg.Gemini
		client, err := genai.NewGemini(ctx, name, cfg.APIKey, cfg.BaseURL, cfg.ProxyURL)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client, Image: client}, nil
	case providerCfg.VertexAI != nil:
		cfg := providerCfg.VertexAI
		client, err := genai.NewVertexAI(ctx, name, cfg.Project, cfg.Location)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.Ark != nil:
		cfg := providerCfg.Ark
		client, err := ark.New(name, cfg.APIKey, cfg.BaseURL)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.OpenAI != nil:
		cfg := providerCfg.OpenAI
		client, err := openai.New(name, cfg.APIKey, cfg.BaseURL)
		if err != nil {
			return provider.Set{}, err
		}
		// OpenAI-compatible 同时承接 chat/completions 与 Images API；
		// gpt-image-2 走后者，文本模型误请求 image 会在供应商侧失败。
		return provider.Set{Text: client, Image: client}, nil
	case providerCfg.LTX != nil:
		cfg := providerCfg.LTX
		client, err := ltx.New(
			name,
			cfg.BaseURL,
			cfg.Token,
			cfg.Duration,
			cfg.FPS,
			cfg.Seed,
			cfg.PollInterval,
			cfg.MaxPollTime,
		)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Video: client}, nil
	case providerCfg.DashScopeVideo != nil:
		cfg := providerCfg.DashScopeVideo
		client, err := dashscopevideo.New(name, cfg.APIKey, cfg.BaseURL, cfg.PollInterval, cfg.MaxPollTime)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Video: client}, nil
	case providerCfg.OminilinkVideo != nil:
		cfg := providerCfg.OminilinkVideo
		client, err := ominilinkvideo.New(name, cfg.APIKey, cfg.BaseURL, cfg.PollInterval, cfg.MaxPollTime)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Video: client}, nil
	case providerCfg.GeminiVideo != nil:
		cfg := providerCfg.GeminiVideo
		client, err := geminivideo.New(name, cfg.APIKey, cfg.BaseURL, cfg.AuthHeader, cfg.PollInterval)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Video: client}, nil
	case providerCfg.ArkVideo != nil:
		cfg := providerCfg.ArkVideo
		client, err := arkvideo.New(name, cfg.APIKey, cfg.BaseURL, cfg.PollInterval, cfg.MaxPollTime)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Video: client}, nil
	default:
		return provider.Set{}, provider.New(provider.ErrorConfiguration, fmt.Sprintf("provider %s has no concrete type", name))
	}
}
