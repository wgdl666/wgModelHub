package vmindgateway

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/wgdl666/kangaroo/callmeta"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
)

// NewRequest 直接构造统一 Gateway 请求；input 由适配器从 protobuf 转换。
func NewRequest(ctx context.Context, baseURL, model string, input any) (*http.Request, error) {
	body, err := httpbody.JSON(map[string]any{"input": input}, true)
	if err != nil {
		return nil, err
	}
	req, err := body.NewRequest(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/gateway/requests/"+url.PathEscape(model))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Idempotency-Key", uuid.NewString())
	callmeta.Propagator{}.Inject(ctx, callmeta.HTTPHeaders(req.Header))
	return req, nil
}
