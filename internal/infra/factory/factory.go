package factory

import (
	"context"
	"fmt"

	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/infra/ark"
	"github.com/wgdl666/wgModelHub/internal/infra/arkvideo"
	"github.com/wgdl666/wgModelHub/internal/infra/bedrockembed"
	"github.com/wgdl666/wgModelHub/internal/infra/bedrockrerank"
	"github.com/wgdl666/wgModelHub/internal/infra/cohereembed"
	"github.com/wgdl666/wgModelHub/internal/infra/coherererank"
	"github.com/wgdl666/wgModelHub/internal/infra/dashscopeembed"
	"github.com/wgdl666/wgModelHub/internal/infra/dashscopererank"
	"github.com/wgdl666/wgModelHub/internal/infra/dashscopevideo"
	"github.com/wgdl666/wgModelHub/internal/infra/elevenlabstts"
	"github.com/wgdl666/wgModelHub/internal/infra/facebodycompare"
	"github.com/wgdl666/wgModelHub/internal/infra/facebodydetect"
	"github.com/wgdl666/wgModelHub/internal/infra/facebodylibrary"
	"github.com/wgdl666/wgModelHub/internal/infra/geminivideo"
	"github.com/wgdl666/wgModelHub/internal/infra/genai"
	"github.com/wgdl666/wgModelHub/internal/infra/humanparser"
	"github.com/wgdl666/wgModelHub/internal/infra/humanyolo"
	"github.com/wgdl666/wgModelHub/internal/infra/ltx"
	"github.com/wgdl666/wgModelHub/internal/infra/minimaxtts"
	"github.com/wgdl666/wgModelHub/internal/infra/ominilinkvideo"
	"github.com/wgdl666/wgModelHub/internal/infra/openai"
	"github.com/wgdl666/wgModelHub/internal/infra/photoroom"
	"github.com/wgdl666/wgModelHub/internal/infra/rekognitioncompare"
	"github.com/wgdl666/wgModelHub/internal/infra/rekognitionfaces"
	"github.com/wgdl666/wgModelHub/internal/infra/rekognitiondetect"
	"github.com/wgdl666/wgModelHub/internal/infra/segmentperson"
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
		client, err := ark.New(name, cfg.APIKey, cfg.BaseURL, cfg.EndpointID)
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
		// GPT Image 2 / 2.5（现网 async_gpt_image）走后者，文本模型误请求 image 会在供应商侧失败。
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
		client, err := geminivideo.New(name, cfg.APIKey, cfg.BaseURL, cfg.AuthHeader, cfg.ProxyURL, cfg.PollInterval)
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
	case providerCfg.MinimaxTTS != nil:
		cfg := providerCfg.MinimaxTTS
		client, err := minimaxtts.New(minimaxtts.Config{
			Name:          name,
			APIKey:        cfg.APIKey,
			Endpoint:      cfg.Endpoint,
			LanguageBoost: cfg.LanguageBoost,
			VoiceID:       cfg.VoiceID,
			Speed:         cfg.Speed,
			Volume:        cfg.Volume,
			Pitch:         cfg.Pitch,
		})
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Speech: client}, nil
	case providerCfg.ElevenLabsTTS != nil:
		cfg := providerCfg.ElevenLabsTTS
		client, err := elevenlabstts.New(elevenlabstts.Config{
			Name:    name,
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			VoiceID: cfg.VoiceID,
		})
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Speech: client}, nil
	case providerCfg.Photoroom != nil:
		cfg := providerCfg.Photoroom
		client, err := photoroom.New(name, cfg.APIKey, cfg.BaseURL)
		if err != nil {
			return provider.Set{}, err
		}
		// 仅暴露 Image：Remove Background 不是文本/视频能力，避免空实现伪装。
		return provider.Set{Image: client}, nil
	case providerCfg.HumanYOLO != nil:
		cfg := providerCfg.HumanYOLO
		client, err := humanyolo.New(name, cfg.BaseURL, cfg.Username, cfg.Password)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.HumanParser != nil:
		cfg := providerCfg.HumanParser
		client, err := humanparser.New(name, cfg.BaseURL, cfg.Username, cfg.Password)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.RekognitionDetect != nil:
		cfg := providerCfg.RekognitionDetect
		client, err := rekognitiondetect.New(ctx, name, cfg.Region, cfg.AccessKeyID, cfg.AccessKeySecret, cfg.SessionToken)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.FacebodyCompare != nil:
		cfg := providerCfg.FacebodyCompare
		client, err := facebodycompare.New(name, cfg.Endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.RekognitionCompare != nil:
		cfg := providerCfg.RekognitionCompare
		client, err := rekognitioncompare.New(ctx, name, cfg.Region, cfg.AccessKeyID, cfg.AccessKeySecret, cfg.SessionToken)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.DashScopeEmbedding != nil:
		cfg := providerCfg.DashScopeEmbedding
		client, err := dashscopeembed.New(name, cfg.BaseURL, cfg.APIKey)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.CohereEmbedding != nil:
		cfg := providerCfg.CohereEmbedding
		client, err := cohereembed.New(name, cfg.BaseURL, cfg.APIKey)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.BedrockEmbedding != nil:
		cfg := providerCfg.BedrockEmbedding
		client, err := bedrockembed.New(ctx, name, cfg.Region, cfg.RoleARN)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.DashScopeRerank != nil:
		cfg := providerCfg.DashScopeRerank
		client, err := dashscopererank.New(name, cfg.BaseURL, cfg.APIKey)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.BedrockRerank != nil:
		cfg := providerCfg.BedrockRerank
		client, err := bedrockrerank.New(ctx, name, cfg.Region, cfg.RoleARN)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.CohereRerank != nil:
		cfg := providerCfg.CohereRerank
		client, err := coherererank.New(name, cfg.BaseURL, cfg.APIKey)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.FacebodyDetect != nil:
		cfg := providerCfg.FacebodyDetect
		client, err := facebodydetect.New(name, cfg.Endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.FacebodyLibrary != nil:
		cfg := providerCfg.FacebodyLibrary
		client, err := facebodylibrary.New(name, cfg.Endpoint, cfg.AccessKeyID, cfg.AccessKeySecret, cfg.Database)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.RekognitionFaces != nil:
		cfg := providerCfg.RekognitionFaces
		client, err := rekognitionfaces.NewDetect(ctx, name, cfg.Region, cfg.AccessKeyID, cfg.AccessKeySecret, cfg.SessionToken)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.RekognitionLibrary != nil:
		cfg := providerCfg.RekognitionLibrary
		client, err := rekognitionfaces.NewLibrary(ctx, name, cfg.Region, cfg.AccessKeyID, cfg.AccessKeySecret, cfg.SessionToken, cfg.Collection)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.SegmentPerson != nil:
		cfg := providerCfg.SegmentPerson
		client, err := segmentperson.New(name, cfg.BaseURL, cfg.Username, cfg.Password, cfg.Method)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Image: client}, nil
	default:
		return provider.Set{}, provider.New(provider.ErrorConfiguration, fmt.Sprintf("provider %s has no concrete type", name))
	}
}
