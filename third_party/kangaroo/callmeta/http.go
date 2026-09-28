package callmeta

import "net/http"

// HTTPHeaders 将统一字段映射为标准连字符 HTTP 头；不能直接发送下划线字段，
// 否则经过默认丢弃下划线请求头的网关时会失去业务关联。gRPC/MQ/持久化仍使用原字段名。
type HTTPHeaders http.Header

func httpKey(key string) string {
	switch key {
	case BusinessKey:
		return "X-WG-Business-Key"
	case BusinessID:
		return "X-WG-Business-Id"
	default:
		return key
	}
}
func (h HTTPHeaders) Get(key string) string { return http.Header(h).Get(httpKey(key)) }
func (h HTTPHeaders) Set(key, value string) { http.Header(h).Set(httpKey(key), value) }
func (h HTTPHeaders) Keys() []string {
	keys := make([]string, 0, len(h))
	for key := range h {
		keys = append(keys, key)
	}
	return keys
}
