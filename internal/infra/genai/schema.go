package genai

// sanitizeGeminiToolSchema 在 Gemini 供应商边界去掉会触发 HTTP 400 的工具 JSON Schema 关键字。
// 实测证据：Hub 实际工具 schema 含 additionalProperties 时 gemini-3.7-flash 拒绝请求；
// 递归删除该字段后，同套 schema 可返回合法工具调用。保留 type/properties/required/items/enum、
// description 以及仍被接受的业务约束（如 minLength/maxLength）。返回深拷贝，不改调用方原文。
func sanitizeGeminiToolSchema(schema any) any {
	if schema == nil {
		return nil
	}
	return sanitizeSchemaNode(schema)
}

func sanitizeSchemaNode(node any) any {
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			// 仅删除供应商已证实拒绝的关键字；property 名本身不是 schema 关键字，不得按白名单过滤。
			if key == "additionalProperties" {
				continue
			}
			if key == "properties" {
				if props, ok := child.(map[string]any); ok {
					copied := make(map[string]any, len(props))
					for propName, propSchema := range props {
						copied[propName] = sanitizeSchemaNode(propSchema)
					}
					out[key] = copied
					continue
				}
			}
			out[key] = sanitizeSchemaNode(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = sanitizeSchemaNode(child)
		}
		return out
	default:
		return v
	}
}
