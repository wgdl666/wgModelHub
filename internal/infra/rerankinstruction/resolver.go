// Package rerankinstruction 将共享模板适配成供应商可接受的指令或 Query，不参与业务门槛和竞速。
package rerankinstruction

import (
	"strings"

	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

// Resolver 每次调用只取一份已校验快照；不缓存模板，允许 AppConfig/Nacos 原子热更新。
type Resolver func() map[string]config.RerankTemplate

func (resolve Resolver) Resolve(query, legacyInstruct, key string, nativeInstruct bool) (string, string, error) {
	if key == "" {
		// 线上旧调用方仍传 instruct；无 key 的请求保持原协议，支持两端分步升级。
		return query, legacyInstruct, nil
	}
	if legacyInstruct != "" {
		return "", "", provider.NotAttempted(provider.ErrorInvalidArgument, "rerank instruction_key and instruct cannot be combined")
	}
	if resolve == nil {
		return "", "", provider.NotAttempted(provider.ErrorConfiguration, "rerank instruction templates are not configured")
	}
	template, ok := resolve()[key]
	if !ok {
		return "", "", provider.NotAttempted(provider.ErrorInvalidArgument, "rerank instruction_key is not configured")
	}
	if nativeInstruct {
		return query, template.Instruct, nil
	}
	// 只替换配置中的占位；Query 中的花括号、JSON 和引号均视为原始文本。
	return strings.ReplaceAll(template.QueryTemplate, "{{query}}", query), "", nil
}
