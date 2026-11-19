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

type ModelDefinition struct {
	Protocol    string `json:"protocol"`
	InputSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	} `json:"input_schema"`
}

// ModelDefinition 读取该环境的协议及可用输入字段。
func (c *Client) ModelDefinition(ctx context.Context, model string) (ModelDefinition, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models/"+url.PathEscape(model), nil)
	if err != nil {
		return ModelDefinition{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	callmeta.Propagator{}.Inject(ctx, callmeta.HTTPHeaders(req.Header))
	response, err := c.http.Do(req)
	if err != nil {
		return ModelDefinition{}, provider.Wrap(provider.ErrorUnavailable, c.name+" read Gateway model definition", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ModelDefinition{}, provider.TakeHTTPError(c.name, response.StatusCode, response.Body)
	}
	var definition ModelDefinition
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&definition); err != nil {
		return ModelDefinition{}, provider.Wrap(provider.ErrorInvalidResponse, c.name+" decode Gateway model definition", err)
	}
	return definition, nil
}
