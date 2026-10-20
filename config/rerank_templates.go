package config

import (
	"fmt"
	"strings"
)

// RerankTemplate 是同一指令的两种协议表示，不按厂商维护两套业务语义。
type RerankTemplate struct {
	Instruct      string `yaml:"instruct"`
	QueryTemplate string `yaml:"query_template"`
}

func validateRerankTemplates(templates map[string]RerankTemplate) error {
	for key, template := range templates {
		if key == "" || strings.TrimSpace(key) != key {
			return fmt.Errorf("rerank_templates contains an empty or untrimmed key")
		}
		if strings.TrimSpace(template.Instruct) == "" {
			return fmt.Errorf("rerank_templates[%s].instruct is required", key)
		}
		// 只支持原始 Query 占位，不执行模板代码；错拼占位符须在配置生效前暴露。
		remaining := strings.ReplaceAll(template.QueryTemplate, "{{query}}", "")
		if !strings.Contains(template.QueryTemplate, "{{query}}") || strings.ContainsAny(remaining, "{}") {
			return fmt.Errorf("rerank_templates[%s].query_template requires {{query}} and no other placeholders", key)
		}
	}
	return nil
}
