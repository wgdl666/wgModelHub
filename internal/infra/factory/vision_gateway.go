package factory

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/wgdl666/kangaroo/callmeta"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
)

func withVisionGateway(baseURL, token string) telemetry.HTTPClientOption {
	return func(client *http.Client) {
		client.Transport = visionGatewayTransport{next: client.Transport, baseURL: baseURL, token: token}
		// 图片和凭据只交给配置的 ModelGateway。遇到重定向保留响应，由适配器处理上游错误。
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
}

type visionGatewayTransport struct {
	next    http.RoundTripper
	baseURL string
	token   string
}

// 旧视觉适配器继续负责 protobuf 与结果转换；统一传输只把内联媒体包装成 JSON input。
func (t visionGatewayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	input, err := gatewayVisionInput(req)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return nil, err
	}
	outbound := req.Clone(req.Context())
	target, err := url.Parse(strings.TrimRight(t.baseURL, "/") + "/v1/gateway/requests/" + url.PathEscape(req.Header.Get("X-Model-Key")))
	if err != nil {
		return nil, err
	}
	outbound.URL = target
	outbound.Body = io.NopCloser(bytes.NewReader(raw))
	outbound.ContentLength = int64(len(raw))
	outbound.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
	outbound.Header.Set("Content-Type", "application/json")
	outbound.Header.Set("Idempotency-Key", uuid.NewString())
	callmeta.Propagator{}.Inject(req.Context(), callmeta.HTTPHeaders(outbound.Header))
	outbound.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(outbound)
}

func gatewayVisionInput(req *http.Request) (any, error) {
	defer req.Body.Close()
	mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	if mediaType == "application/json" {
		var input json.RawMessage
		err := json.NewDecoder(req.Body).Decode(&input)
		return input, err
	}
	if mediaType != "multipart/form-data" {
		return nil, fmt.Errorf("unsupported gateway vision content type: %s", mediaType)
	}
	input := map[string]string{}
	reader := multipart.NewReader(req.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		value, err := io.ReadAll(part)
		part.Close()
		if err != nil {
			return nil, err
		}
		if part.FileName() != "" {
			input[part.FormName()] = base64.StdEncoding.EncodeToString(value)
		} else {
			input[part.FormName()] = string(value)
		}
	}
	return input, nil
}
