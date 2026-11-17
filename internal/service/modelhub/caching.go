package modelhub

import (
	"strings"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/llmmetric"
	"github.com/wgdl666/wgModelHub/models"
)

// 缓存配置模式写入指标分类；configured/strategy != hit。
// implicit_automatic 表示未下发显式 caching 开关，供应商隐式缓存自动生效；
// 是否命中仍只看 usage.cached_tokens > 0。
const (
	cachingModeDefaultEnabled    = "default_enabled"
	cachingModeExplicitEnabled   = "explicit_enabled"
	cachingModeExplicitDisabled  = llmmetric.CachingModeExplicitDisabled
	cachingModeImplicitAutomatic = "implicit_automatic"
)

// textCachingMode 区分缺省开启与调用方显式开关（应用策略之前的请求侧语义）。
// 开启只表示请求侧配置；是否命中必须以 usage.cached_tokens > 0 为准，二者不可混读。
func textCachingMode(input *modelhubv2.Input) string {
	if input == nil || input.Caching == nil {
		return cachingModeDefaultEnabled
	}
	if input.Caching.Enabled {
		return cachingModeExplicitEnabled
	}
	return cachingModeExplicitDisabled
}

// arkEndpointBound 判断 Ark 是否绑定了推理 endpoint。
// 此类部署走官方隐式缓存（自动开启、不可关闭），未开通显式 caching 能力。
func arkEndpointBound(providerCfg config.ProviderConfig) bool {
	return providerCfg.Ark != nil && strings.TrimSpace(providerCfg.Ark.EndpointID) != ""
}

// textCachingModeForProvider 只计算遥测模式；适配器把缓存策略写入自己的上游参数，不修改共享请求。
func textCachingModeForProvider(request *modelhubv2.GenerateRequest, providerCfg config.ProviderConfig) string {
	if arkEndpointBound(providerCfg) || ((request.GetModel() == models.Gemini4Argon || request.GetModel() == models.Gemini4Carbon) && (providerCfg.Gemini != nil || providerCfg.VertexAI != nil)) {
		return cachingModeImplicitAutomatic
	}
	return textCachingMode(request.GetInput())
}
