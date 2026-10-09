package factory

import (
	"context"
	"fmt"

	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/infra/ark"
	"github.com/wgdl666/wgModelHub/internal/infra/arkvideo"
	aliyunasr "github.com/wgdl666/wgModelHub/internal/infra/asr/aliyun"
	assemblyaiasr "github.com/wgdl666/wgModelHub/internal/infra/asr/assemblyai"
	deepgramasr "github.com/wgdl666/wgModelHub/internal/infra/asr/deepgram"
	funasr "github.com/wgdl666/wgModelHub/internal/infra/asr/funasr"
	qwenasr "github.com/wgdl666/wgModelHub/internal/infra/asr/qwen"
	sonioxasr "github.com/wgdl666/wgModelHub/internal/infra/asr/soniox"
	tencentasr "github.com/wgdl666/wgModelHub/internal/infra/asr/tencent"
	volcengineasr "github.com/wgdl666/wgModelHub/internal/infra/asr/volcengine"
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
	"github.com/wgdl666/wgModelHub/internal/infra/fashion"
	"github.com/wgdl666/wgModelHub/internal/infra/geminivideo"
	"github.com/wgdl666/wgModelHub/internal/infra/genai"
	"github.com/wgdl666/wgModelHub/internal/infra/humanparser"
	"github.com/wgdl666/wgModelHub/internal/infra/humanyolo"
	"github.com/wgdl666/wgModelHub/internal/infra/imageseg"
	"github.com/wgdl666/wgModelHub/internal/infra/ltx"
	"github.com/wgdl666/wgModelHub/internal/infra/minimaxtts"
	"github.com/wgdl666/wgModelHub/internal/infra/mixedbreadrerank"
	"github.com/wgdl666/wgModelHub/internal/infra/ominilinkvideo"
	"github.com/wgdl666/wgModelHub/internal/infra/openai"
	"github.com/wgdl666/wgModelHub/internal/infra/photoroom"
	"github.com/wgdl666/wgModelHub/internal/infra/rekognitioncompare"
	"github.com/wgdl666/wgModelHub/internal/infra/rekognitiondetect"
	"github.com/wgdl666/wgModelHub/internal/infra/rekognitionfaces"
	"github.com/wgdl666/wgModelHub/internal/infra/segmentperson"
	"github.com/wgdl666/wgModelHub/internal/infra/vworldimage"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

// Build 按配置实例化供应商能力；真实模型路由在 service 层完成，这里只负责连接与能力装配。
func Build(ctx context.Context, cfg config.Config) (map[string]provider.Set, error) {
	return build(ctx, cfg, nil)
}

// BuildLive 与 Build 相同，但薇光生图会在每次请求重读 live 里的 base_url。
// 其他供应商仍使用启动时的连接参数；那些字段变化继续由 LiveConfig 拒绝。
func BuildLive(ctx context.Context, live *config.LiveConfig) (map[string]provider.Set, error) {
	if live == nil {
		return nil, fmt.Errorf("live config is required")
	}
	return build(ctx, live.Load(), live)
}

func build(ctx context.Context, cfg config.Config, live *config.LiveConfig) (map[string]provider.Set, error) {
	sets := make(map[string]provider.Set, len(cfg.Providers))
	for name, providerCfg := range cfg.Providers {
		if config.IsASRProvider(providerCfg) && !config.ASRCredentialsPresent(providerCfg) {
			// 多环境共用配置模板时，只实例化本区真正有凭据的 ASR，避免空密钥占用模型路由。
			continue
		}
		set, err := buildProvider(ctx, name, providerCfg, live)
		if err != nil {
			return nil, fmt.Errorf("provider %s: %w", name, err)
		}
		sets[name] = set
	}
	return sets, nil
}

func buildProvider(ctx context.Context, name string, providerCfg config.ProviderConfig, live *config.LiveConfig) (provider.Set, error) {
	switch {
	case providerCfg.VolcengineASR != nil:
		cfg := providerCfg.VolcengineASR
		client, err := volcengineasr.New(volcengineasr.Config{AppKey: cfg.AppKey, AccessKey: cfg.AccessKey, APIKey: cfg.APIKey, ResourceID: cfg.ResourceID, URL: cfg.URL})
		return provider.Set{ASR: client}, err
	case providerCfg.AliyunASR != nil:
		cfg := providerCfg.AliyunASR
		client, err := aliyunasr.New(aliyunasr.Config{AKID: cfg.AKID, AKKey: cfg.AKKey, AppKey: cfg.AppKey, Token: cfg.Token, URL: cfg.URL})
		return provider.Set{ASR: client}, err
	case providerCfg.FunASR != nil:
		cfg := providerCfg.FunASR
		client, err := funasr.New(funasr.Config{APIKey: cfg.APIKey, WorkspaceID: cfg.WorkspaceID, URL: cfg.URL})
		return provider.Set{ASR: client}, err
	case providerCfg.QwenASR != nil:
		cfg := providerCfg.QwenASR
		client, err := qwenasr.New(qwenasr.Config{APIKey: cfg.APIKey, WorkspaceID: cfg.WorkspaceID, URL: cfg.URL})
		return provider.Set{ASR: client}, err
	case providerCfg.TencentASR != nil:
		cfg := providerCfg.TencentASR
		client, err := tencentasr.New(tencentasr.Config{AppID: cfg.AppID, SecretID: cfg.SecretID, SecretKey: cfg.SecretKey, ProxyURL: cfg.ProxyURL})
		return provider.Set{ASR: client}, err
	case providerCfg.AssemblyAIASR != nil:
		cfg := providerCfg.AssemblyAIASR
		client, err := assemblyaiasr.New(assemblyaiasr.Config{APIKey: cfg.APIKey, URL: cfg.URL})
		return provider.Set{ASR: client}, err
	case providerCfg.DeepgramASR != nil:
		cfg := providerCfg.DeepgramASR
		client, err := deepgramasr.New(deepgramasr.Config{APIKey: cfg.APIKey, URL: cfg.URL})
		return provider.Set{ASR: client}, err
	case providerCfg.SonioxASR != nil:
		cfg := providerCfg.SonioxASR
		client, err := sonioxasr.New(sonioxasr.Config{APIKey: cfg.APIKey, URL: cfg.URL})
		return provider.Set{ASR: client}, err
	case providerCfg.ImageSeg != nil:
		cfg := providerCfg.ImageSeg
		client, err := imageseg.New(cfg.Endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Image: client}, nil
	case providerCfg.Fashion != nil:
		cfg := providerCfg.Fashion
		client, err := fashion.New(name, cfg.BaseURL, cfg.Username, cfg.Password)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client}, nil
	case providerCfg.Gemini != nil:
		cfg := providerCfg.Gemini
		client, err := genai.NewGemini(ctx, name, cfg.APIKey, cfg.BaseURL, cfg.ProxyURL)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Text: client, Image: client}, nil
	case providerCfg.VertexAI != nil:
		cfg := providerCfg.VertexAI
		client, err := genai.NewVertexAI(ctx, name, cfg.APIKey)
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
		)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Video: client}, nil
	case providerCfg.DashScopeVideo != nil:
		cfg := providerCfg.DashScopeVideo
		client, err := dashscopevideo.New(name, cfg.APIKey, cfg.BaseURL, cfg.PollInterval)
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Video: client}, nil
	case providerCfg.OminilinkVideo != nil:
		cfg := providerCfg.OminilinkVideo
		client, err := ominilinkvideo.New(name, cfg.APIKey, cfg.BaseURL, cfg.PollInterval)
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
		client, err := arkvideo.New(name, cfg.APIKey, cfg.BaseURL, cfg.PollInterval)
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
		// 口播滑条来自部署配置（如路演 AppConfig），启动时装入 provider，变更需滚动重启。
		client, err := elevenlabstts.New(elevenlabstts.Config{
			Name:    name,
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			VoiceID: cfg.VoiceID,
			VoiceSettings: elevenlabstts.VoiceSettings{
				Stability:       cfg.Stability,
				SimilarityBoost: cfg.SimilarityBoost,
				Style:           cfg.Style,
				Speed:           cfg.Speed,
			},
		})
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Speech: client}, nil
	case providerCfg.Photoroom != nil:
		cfg := providerCfg.Photoroom
		client, err := photoroom.New(name, cfg.APIKey, cfg.BaseURL, cfg.ProxyURL)
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
	case providerCfg.MixedbreadRerank != nil:
		cfg := providerCfg.MixedbreadRerank
		client, err := mixedbreadrerank.New(name, cfg.BaseURL, cfg.APIKey)
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
	case providerCfg.VWorldImage != nil:
		cfg := providerCfg.VWorldImage
		var client *vworldimage.Provider
		var err error
		if live != nil {
			// 闭包按供应商名读当前配置。只换 base_url 时下一次生图走新地址，不必滚动重启。
			providerName := name
			fallback := cfg.BaseURL
			client, err = vworldimage.NewResolving(name, cfg.Username, cfg.Password, func() string {
				current, ok := live.Load().Providers[providerName]
				if !ok || current.VWorldImage == nil {
					return fallback
				}
				return current.VWorldImage.BaseURL
			})
		} else {
			client, err = vworldimage.New(name, cfg.BaseURL, cfg.Username, cfg.Password)
		}
		if err != nil {
			return provider.Set{}, err
		}
		return provider.Set{Image: client}, nil
	default:
		return provider.Set{}, provider.New(provider.ErrorConfiguration, fmt.Sprintf("provider %s has no concrete type", name))
	}
}
