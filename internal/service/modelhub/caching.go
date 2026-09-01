package modelhub

import (
	"context"
	"strings"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// 缓存配置模式写入 span，供 wgMonitor 按真实模型聚合；configured/strategy != hit。
// implicit_automatic 表示未下发显式 caching 开关，供应商隐式缓存自动生效；
// 是否命中仍只看 usage.cached_tokens > 0。
const (
	cachingModeDefaultEnabled    = "default_enabled"
	cachingModeExplicitEnabled   = "explicit_enabled"
	cachingModeExplicitDisabled  = "explicit_disabled"
	cachingModeImplicitAutomatic = "implicit_automatic"
)

// OTel 属性名与 wgMonitor Logfire SQL 保持一致；禁止写入 prompt/媒体/密钥。
const (
	attrCapability     = "modelhub.capability"
	attrModel          = "modelhub.model"
	attrProvider       = "modelhub.provider"
	attrCachingMode    = "modelhub.caching.mode"
	attrUsagePresent   = "modelhub.usage.present"
	attrInputTokens    = "modelhub.usage.input_tokens"
	attrCachedTokens   = "modelhub.usage.cached_tokens"
	attrCacheHit       = "modelhub.cache.hit"
	capabilityTextAttr = "text"
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

// recordTextGenerateTelemetry 在供应商成功返回后、客户端最终 Send 前写入终态属性，
// 避免 Send 失败导致已完成的 usage 从 span 丢失；流式与非流式共用同一套字段。
func recordTextGenerateTelemetry(ctx context.Context, model, providerName, cachingMode string, usage *modelhubv2.Usage) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	present := usage != nil
	var inputTokens, cachedTokens int64
	if present {
		inputTokens = usage.GetInputTokens()
		cachedTokens = usage.GetCachedTokens()
	}
	// hit 只看供应商归一化后的 cached_tokens，不把请求开启缓存当成命中。
	span.SetAttributes(
		attribute.String(attrCapability, capabilityTextAttr),
		attribute.String(attrModel, model),
		attribute.String(attrProvider, providerName),
		attribute.String(attrCachingMode, cachingMode),
		attribute.Bool(attrUsagePresent, present),
		attribute.Int64(attrInputTokens, inputTokens),
		attribute.Int64(attrCachedTokens, cachedTokens),
		attribute.Bool(attrCacheHit, cachedTokens > 0),
	)
}
