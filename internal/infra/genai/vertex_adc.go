package genai

import (
	"context"
	"net/http"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"github.com/wgdl666/wgModelHub/internal/provider"
	genaisdk "google.golang.org/genai"
)

func newADCVertexClient(ctx context.Context, name, project string, httpClient *http.Client) (*genaisdk.Client, *http.Client, error) {
	// 使用原始 HTTP client 换 token，保留代理和观测；不能让已带鉴权的 client 递归获取 token。
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
		Client: httpClient,
	})
	if err != nil {
		// SDK 的凭据解析错误可能携带 JSON/私钥片段，禁止回显原文。
		return nil, nil, provider.New(provider.ErrorConfiguration, name+" ADC credentials are unavailable or invalid")
	}
	authed := *httpClient
	authed.Transport = &vertexADCTransport{base: httpClient.Transport, tokens: creds.TokenProvider, project: project}
	client, err := genaisdk.NewClient(ctx, &genaisdk.ClientConfig{
		Backend: genaisdk.BackendVertexAI, Project: project, Location: "global",
		Credentials: creds, HTTPClient: &authed,
	})
	if err != nil {
		return nil, nil, provider.New(provider.ErrorConfiguration, "create "+name+" ADC client failed")
	}
	return client, &authed, nil
}

// 自带 HTTP client 时 GenAI SDK 不会补鉴权。ADC 缓存/刷新 token，模型 API使用 Bearer。
type vertexADCTransport struct {
	base    http.RoundTripper
	tokens  auth.TokenProvider
	project string
}

func (t *vertexADCTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 视频首帧和签名下载地址可能在第三方存储，ADC 只能发给 Google API。
	if req.URL.Scheme != "https" || req.URL.Host != "aiplatform.googleapis.com" {
		return t.base.RoundTrip(req)
	}
	token, err := t.tokens.Token(req.Context())
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		// 上游换 token 的错误正文可能回显身份材料；只返回稳定分类，且不能记为模型已调用。
		return nil, provider.NotAttempted(provider.ErrorConfiguration, "vertex could not acquire ADC access token")
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+token.Value)
	clone.Header.Set("X-Goog-User-Project", t.project)
	return t.base.RoundTrip(clone)
}
