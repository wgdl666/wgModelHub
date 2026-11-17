package vmindgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/wgdl666/kangaroo/callmeta"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

// ModelProtocol 读取模型的输入适配类型；模型键与协议的映射只在 Gateway 登记。
func (c *Client) ModelProtocol(ctx context.Context, model string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models/"+url.PathEscape(model), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	callmeta.Propagator{}.Inject(ctx, callmeta.HTTPHeaders(req.Header))
	response, err := c.http.Do(req)
	if err != nil {
		return "", provider.Wrap(provider.ErrorUnavailable, c.name+" read Gateway model definition", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", provider.TakeHTTPError(c.name, response.StatusCode, response.Body)
	}
	var definition struct {
		Protocol string `json:"protocol"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&definition); err != nil {
		return "", provider.Wrap(provider.ErrorInvalidResponse, c.name+" decode Gateway model definition", err)
	}
	return definition.Protocol, nil
}
