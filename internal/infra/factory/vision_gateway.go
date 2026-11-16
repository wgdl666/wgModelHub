package factory

import (
	"net/http"

	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
)

func withVisionGatewayAuth(token string) telemetry.HTTPClientOption {
	return func(client *http.Client) {
		client.Transport = visionGatewayAuth{next: client.Transport, token: token}
		// 图片和凭据只交给配置的 Gateway，保留重定向响应供适配器处理。
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
}

type visionGatewayAuth struct {
	next  http.RoundTripper
	token string
}

func (t visionGatewayAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	outbound := req.Clone(req.Context())
	outbound.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(outbound)
}
