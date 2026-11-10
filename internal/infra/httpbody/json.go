package httpbody

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sort"
)

// JSON composes acyclic request maps assembled by adapters. Large ASCII strings
// (including Base64) are referenced directly; other leaves use encoding/json.
// escapeHTML matches the provider's existing json.Encoder policy.
func JSON(value any, escapeHTML bool) (*Body, error) {
	body := &Body{ContentType: "application/json"}
	if err := body.appendJSON(value, escapeHTML); err != nil {
		return nil, err
	}
	return body, nil
}

func (b *Body) appendJSON(value any, escapeHTML bool) error {
	switch value := value.(type) {
	case string:
		if safeASCII(value, escapeHTML) {
			b.addString(`"`)
			b.addString(value)
			b.addString(`"`)
			return nil
		}
	case []byte:
		if value == nil {
			b.addString("null")
			return nil
		}
		return b.appendJSON(base64.StdEncoding.EncodeToString(value), escapeHTML)
	case map[string]any:
		if value == nil {
			b.addString("null")
			return nil
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.addString("{")
		for i, key := range keys {
			if i > 0 {
				b.addString(",")
			}
			if err := b.appendJSON(key, escapeHTML); err != nil {
				return err
			}
			b.addString(":")
			if err := b.appendJSON(value[key], escapeHTML); err != nil {
				return err
			}
		}
		b.addString("}")
		return nil
	case map[string]string:
		if value == nil {
			b.addString("null")
			return nil
		}
		fields := make(map[string]any, len(value))
		for key, text := range value {
			fields[key] = text
		}
		return b.appendJSON(fields, escapeHTML)
	case []any:
		if value == nil {
			b.addString("null")
			return nil
		}
		return b.appendArray(len(value), func(i int) any { return value[i] }, escapeHTML)
	case []string:
		if value == nil {
			b.addString("null")
			return nil
		}
		return b.appendArray(len(value), func(i int) any { return value[i] }, escapeHTML)
	case []map[string]any:
		if value == nil {
			b.addString("null")
			return nil
		}
		return b.appendArray(len(value), func(i int) any { return value[i] }, escapeHTML)
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(escapeHTML)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	data := encoded.Bytes()
	b.addBytes(data[:len(data)-1]) // Encoder adds a newline; fragments must not.
	return nil
}

func (b *Body) appendArray(n int, at func(int) any, escapeHTML bool) error {
	b.addString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.addString(",")
		}
		if err := b.appendJSON(at(i), escapeHTML); err != nil {
			return err
		}
	}
	b.addString("]")
	return nil
}

func safeASCII(text string, escapeHTML bool) bool {
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c < 0x20 || c >= 0x80 || c == '"' || c == '\\' || (escapeHTML && (c == '<' || c == '>' || c == '&')) {
			return false
		}
	}
	return true
}
