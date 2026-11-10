package factory

import (
	"net/http"

	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
)

func withVisionGatewayAuth(token string) telemetry.HTTPClientOption {
	return func(client *http.Client) {
		client.Transport = visionGatewayTransport{next: client.Transport, token: token}
		// 图片和凭据只交给配置的 ModelGateway。遇到重定向保留响应，由适配器处理上游错误。
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
}

type visionGatewayTransport struct {
	next  http.RoundTripper
	token string
}

func (t visionGatewayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	outbound := req.Clone(req.Context())
	outbound.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(outbound)
}
