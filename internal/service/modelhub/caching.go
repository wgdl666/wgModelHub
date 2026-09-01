package modelhub

import (
	"context"
	"strings"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// 缓存配置模式写入 span，供 wgMonitor 按真实模型聚合；configured != hit。
const (
	cachingModeDefaultEnabled   = "default_enabled"
	cachingModeExplicitEnabled  = "explicit_enabled"
	cachingModeExplicitDisabled = "explicit_disabled"
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

// textCachingMode 区分缺省开启与调用方显式开关。
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

// applyTextCachingDefault 仅服务文本 Generate：省略 Input.Caching 时写入 enabled=true，
// 让 Ark 等需要显式开关的供应商真正启用，降低重复前缀成本；显式 enabled=false 必须保留。
// endpoint_id 绑定的推理部署通常未开通 Ark 缓存服务，缺省开启会 403，故改为显式 disabled。
func applyTextCachingDefault(request *modelhubv2.GenerateRequest, providerCfg config.ProviderConfig) {
	// 调用点仅限已通过 validateGenerateRequest 的 generateText；request 非空契约已成立。
	if request.Input == nil {
		request.Input = &modelhubv2.Input{}
	}
	if request.Input.Caching == nil {
		if providerCfg.Ark != nil && strings.TrimSpace(providerCfg.Ark.EndpointID) != "" {
			request.Input.Caching = &modelhubv2.CachingConfig{Enabled: false}
			return
		}
		request.Input.Caching = &modelhubv2.CachingConfig{Enabled: true}
	}
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
