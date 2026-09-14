package modelhub

import (
	"strings"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/llmmetric"
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

// applyTextCachingPolicy 仅服务文本 Generate 的缓存策略：
//   - 普通 provider：省略 Input.Caching 时写入 enabled=true，显式 true/false 原样保留。
//   - endpoint_id 绑定的 Ark：无论调用方省略 / 显式 true / 显式 false，一律清空 Caching，
//     不下发显式 enabled 或 expire_at，从而使用官方自动隐式缓存（避免显式缓存 403）。
//
// 返回值是实际生效的遥测模式；configured/strategy != hit。
func applyTextCachingPolicy(request *modelhubv2.GenerateRequest, providerCfg config.ProviderConfig) string {
	// 调用点仅限已通过 validateGenerateRequest 的 generateText；request 非空契约已成立。
	callerMode := textCachingMode(request.GetInput())
	if request.Input == nil {
		request.Input = &modelhubv2.Input{}
	}
	if arkEndpointBound(providerCfg) {
		// 清空字段：Ark provider 仅在 Caching.Enabled=true 时下发显式 caching；
		// nil 表示走隐式缓存，不得写成 enabled=false（那会误读成「关闭缓存」）。
		request.Input.Caching = nil
		return cachingModeImplicitAutomatic
	}
	if request.Input.Caching == nil {
		request.Input.Caching = &modelhubv2.CachingConfig{Enabled: true}
	}
	return callerMode
}
