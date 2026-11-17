package factory

import (
	"github.com/wgdl666/kangaroo/callmeta"
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
	// 只对已选择的内部视觉 Gateway 注入来源身份。
	callmeta.Propagator{}.Inject(req.Context(), callmeta.HTTPHeaders(outbound.Header))
	outbound.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(outbound)
}
