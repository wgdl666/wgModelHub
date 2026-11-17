package channel

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/wgdl666/wgModelHub/config"
	"gopkg.in/yaml.v3"
)

// RewriteYAML 只改 providers 的键及 model_routes、vision_gateway 对这些键的引用。
// 其余节点原样保留，包括未知字段、注释和凭据。输入输出都是本地 YAML，不访问网络。
// 新键冲突、路由指向不存在的实例，或一份文件里有多段文档时拒绝写出。
func RewriteYAML(content []byte) ([]byte, map[string]string, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, nil, fmt.Errorf("YAML contains multiple documents")
		}
		return nil, nil, err
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("YAML root must be a mapping")
	}
	var parsed config.Config
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		return nil, nil, err
	}
	renames, err := Names(parsed.Providers)
	if err != nil {
		return nil, nil, err
	}
	providers := mappingChild(root, "providers")
	if providers == nil {
		if len(renames) != 0 {
			return nil, nil, fmt.Errorf("providers are missing")
		}
	} else {
		if err := renameMappingKeys(providers, renames); err != nil {
			return nil, nil, err
		}
		for i := 1; i < len(providers.Content); i += 2 {
			// 网关引用也要随实例键改名，否则发布后无法找到该实例的地址。
			if ref := mappingChild(providers.Content[i], "vision_gateway"); ref != nil && strings.TrimSpace(ref.Value) != "" {
				if err := replaceProviderScalar(ref, renames); err != nil {
					return nil, nil, fmt.Errorf("vision_gateway: %w", err)
				}
			}
		}
	}
	if routes := mappingChild(root, "model_routes"); routes != nil {
		if err := renameRoutes(routes, renames); err != nil {
			return nil, nil, err
		}
	}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	// 编码整个文档节点，才能带上文件头和文末注释。
	if err := encoder.Encode(&document); err != nil {
		return nil, nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), renames, nil
}

func mappingChild(root *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return root.Content[i+1]
		}
	}
	return nil
}

func renameMappingKeys(node *yaml.Node, renames map[string]string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("providers must be a mapping")
	}
	seen := map[string]struct{}{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode {
			return fmt.Errorf("provider key must be a string")
		}
		next, ok := renames[key.Value]
		if !ok {
			return fmt.Errorf("provider %s has no rename", key.Value)
		}
		if _, dup := seen[next]; dup {
			return fmt.Errorf("instance name %q is claimed more than once", next)
		}
		seen[next] = struct{}{}
		key.Value = next
		if yamlKeyNeedsQuotes(next) {
			key.Style = yaml.DoubleQuotedStyle
		}
	}
	return nil
}

func renameRoutes(node *yaml.Node, renames map[string]string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("model_routes must be a mapping")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		value := node.Content[i+1]
		switch value.Kind {
		case yaml.ScalarNode:
			if err := replaceProviderScalar(value, renames); err != nil {
				return fmt.Errorf("model_routes[%s]: %w", node.Content[i].Value, err)
			}
		case yaml.MappingNode:
			if err := renameRouteObject(value, renames); err != nil {
				return fmt.Errorf("model_routes[%s]: %w", node.Content[i].Value, err)
			}
		default:
			return fmt.Errorf("model_routes[%s] must be a provider name or default/race object", node.Content[i].Value)
		}
	}
	return nil
}

func renameRouteObject(node *yaml.Node, renames map[string]string) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		field := node.Content[i].Value
		child := node.Content[i+1]
		switch field {
		case "default":
			if child.Kind != yaml.ScalarNode {
				return fmt.Errorf("default must be a provider name")
			}
			if err := replaceProviderScalar(child, renames); err != nil {
				return err
			}
		case "race":
			if child.Kind != yaml.SequenceNode {
				return fmt.Errorf("race must be a list")
			}
			for _, item := range child.Content {
				if item.Kind != yaml.ScalarNode {
					return fmt.Errorf("race item must be a provider name")
				}
				if err := replaceProviderScalar(item, renames); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func replaceProviderScalar(node *yaml.Node, renames map[string]string) error {
	next, ok := renames[strings.TrimSpace(node.Value)]
	if !ok {
		return fmt.Errorf("unknown provider %s", node.Value)
	}
	node.Value = next
	if yamlKeyNeedsQuotes(next) {
		node.Style = yaml.DoubleQuotedStyle
	}
	return nil
}

func yamlKeyNeedsQuotes(value string) bool {
	return strings.ContainsAny(value, ":{}[]&*!|>%@`#,'\"") || strings.Contains(value, " ")
}
