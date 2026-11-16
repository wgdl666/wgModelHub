package vmindgateway

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/wgdl666/kangaroo/callmeta"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

// Invoke 提交一次同步模型请求；不重试可能已被 Gateway 受理的调用。
func (c *Client) Invoke(ctx context.Context, model string, input any, timeout time.Duration) (*http.Response, error) {
	body, err := httpbody.JSON(map[string]any{"input": input}, true)
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "encode Gateway request", err)
	}
	req, err := body.NewRequest(ctx, http.MethodPost, c.baseURL+"/v1/gateway/requests/"+url.PathEscape(model))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Idempotency-Key", uuid.NewString())
	callmeta.Propagator{}.Inject(ctx, callmeta.HTTPHeaders(req.Header))
	req.Header.Set("Authorization", "Bearer "+c.token)
	client := *c.http
	client.Timeout = timeout
	return client.Do(req)
}
