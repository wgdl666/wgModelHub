// Package vmindgateway 承载 Go 网关的 JSON 协议，成品读取只使用配置好的存储 SDK。
package vmindgateway

import (
	"context"
	"encoding/json"
	"github.com/wgdl666/kangaroo/callmeta"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/infra/objectstore"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

type Image struct {
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data"`
}

type Task struct {
	RequestID string `json:"request_id"`
	State     string `json:"state"`
	Result    struct {
		Files []objectstore.Reference `json:"files"`
	} `json:"result"`
}

type Client struct {
	name, baseURL, token string
	http                 *http.Client
	objects              *objectstore.S3
}

func New(name, baseURL, token string, objects *objectstore.S3) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.User != nil || endpoint.ForceQuery || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		strings.TrimSpace(token) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "VMind Go gateway URL and token are required")
	}
	client := telemetry.NewHTTPClient()
	client.Timeout = 125 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{name: name, baseURL: baseURL, token: strings.TrimSpace(token), http: client, objects: objects}, nil
}

func (c *Client) Submit(ctx context.Context, model, id string, input map[string]any, accepted int) (Task, error) {
	if id == "" {
		id = uuid.NewString()
	}
	// Trace 与业务输入分离，重复查询可以按同一 UUID 找回执行和制品。
	// Expose media leaves to the body composer without changing the caller's input map.
	if images, ok := input["images"].([]Image); ok {
		input = maps.Clone(input)
		var values []map[string]any
		if images != nil {
			values = make([]map[string]any, len(images))
		}
		for i, image := range images {
			values[i] = map[string]any{"mime_type": image.MIMEType, "data": image.Data}
		}
		input["images"] = values
	}
	// 输出能力随同一次提交传递，网关必须在入队前拒绝图像/视频协议不匹配。
	output := "image"
	if accepted == http.StatusAccepted {
		output = "video"
	}
	body, err := httpbody.JSON(map[string]any{
		"input": input, "output": output, "metadata": map[string]string{"trace_id": telemetry.TraceID(ctx)},
	}, true)
	if err != nil {
		return Task{}, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "encode VMind gateway input", err)
	}
	return c.call(ctx, http.MethodPost, "/v1/gateway/requests/"+url.PathEscape(model), id, body, accepted)
}

func (c *Client) Get(ctx context.Context, id string) (Task, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return Task{}, provider.New(provider.ErrorInvalidArgument, "invalid VMind gateway request ID")
	}
	return c.call(ctx, http.MethodGet, "/v1/gateway/requests", id, nil, http.StatusOK)
}

func (c *Client) call(ctx context.Context, method, path, id string, body *httpbody.Body, expected int) (Task, error) {
	req, err := body.NewRequest(ctx, method, c.baseURL+path)
	if err != nil {
		return Task{}, provider.Wrap(provider.ErrorConfiguration, c.name+" create request", err)
	}
	// 仅内部 Gateway 接收来源身份；通用供应商 HTTP transport 不传播用户信息。
	callmeta.Propagator{}.Inject(ctx, callmeta.HTTPHeaders(req.Header))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Idempotency-Key", id)
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Task{}, ctx.Err()
		}
		return Task{}, provider.Wrap(provider.ErrorUnavailable, c.name+" gateway call failed", err)
	}
	defer res.Body.Close()
	if res.StatusCode != expected {
		return Task{}, provider.TakeHTTPError(c.name, res.StatusCode, res.Body)
	}
	var task Task
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&task) != nil || task.RequestID != id {
		return Task{}, provider.New(provider.ErrorInvalidResponse, c.name+" returned an invalid gateway task")
	}
	switch task.State {
	case "queued", "processing", "succeeded", "failed":
		return task, nil
	default:
		return Task{}, provider.New(provider.ErrorInvalidResponse, c.name+" returned an invalid gateway state")
	}
}

func (c *Client) OpenResult(ctx context.Context, id string, ref objectstore.Reference) (string, io.ReadCloser, error) {
	// 视觉协议查询客户端不配置对象存储，媒体读取必须使用完整的网关连接。
	if c.objects == nil {
		return "", nil, provider.New(provider.ErrorConfiguration, "gateway result storage is not configured")
	}
	// 适配器以请求 UUID 划分对象前缀，禁止一个任务引用其他任务的成品。
	if !strings.HasPrefix(ref.Key, "outputs/"+id+"/") {
		return "", nil, provider.New(provider.ErrorInvalidResponse, "VMind result object belongs to another request")
	}
	contentType, reader, err := c.objects.OpenReference(ctx, ref)
	if err != nil {
		return "", nil, provider.New(provider.ErrorUnavailable, "cannot open VMind result object")
	}
	return contentType, reader, nil
}
