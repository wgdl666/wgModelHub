package genai

import (
	"context"
	"net/http"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"github.com/wgdl666/wgModelHub/internal/provider"
	genaisdk "google.golang.org/genai"
)

func newADCVertexClient(ctx context.Context, name, project string, httpClient *http.Client) (*genaisdk.Client, error) {
	// 使用原始 HTTP client 换 token，保留代理和观测；不能让已带鉴权的 client 递归获取 token。
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
		Client: httpClient,
	})
	if err != nil {
		// SDK 的凭据解析错误可能携带 JSON/私钥片段，禁止回显原文。
		return nil, provider.New(provider.ErrorConfiguration, name+" ADC credentials are unavailable or invalid")
	}
	authed := *httpClient
	authed.Transport = &vertexADCTransport{base: httpClient.Transport, tokens: creds.TokenProvider, project: project}
	client, err := genaisdk.NewClient(ctx, &genaisdk.ClientConfig{
		Backend: genaisdk.BackendVertexAI, Project: project, Location: "global",
		Credentials: creds, HTTPClient: &authed,
	})
	if err != nil {
		return nil, provider.New(provider.ErrorConfiguration, "create "+name+" ADC client failed")
	}
	return client, nil
}

// 自带 HTTP client 时 GenAI SDK 不会补鉴权。token 的缓存刷新由 ADC 管理，模型请求只加 Bearer。
type vertexADCTransport struct {
	base    http.RoundTripper
	tokens  auth.TokenProvider
	project string
}

func (t *vertexADCTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := vertexAccessToken(req.Context(), t.tokens)
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

// auth v0.9.3 对凭据文件建缓存时不传 DisableAsyncRefresh：令牌进入过期前 3m45s 窗口后，
// 缓存仍把旧令牌交给本次请求，只在后台换新。低流量时整点请求与 1 小时有效期对齐，
// 旧令牌经代理到达 Vertex 时已过期，返回 401。剩余不足余量时等后台换出更晚过期的令牌再发。
// 升级到凭据文件也支持 DisableAsyncRefresh 的 auth 版本后，应改用该选项并删除这层等待。
var (
	vertexTokenMinRemaining = 10 * time.Second
	vertexTokenPollInterval = 50 * time.Millisecond
)

func vertexAccessToken(ctx context.Context, tokens auth.TokenProvider) (*auth.Token, error) {
	token, err := tokens.Token(ctx)
	if err != nil || token.Expiry.IsZero() || time.Until(token.Expiry) > vertexTokenMinRemaining {
		return token, err
	}
	stale := token.Expiry
	timer := time.NewTimer(vertexTokenPollInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
		// 旧令牌过期后缓存转为同步换新，这次取到的结果即为最终结果，不再等待。
		expired := time.Now().After(stale)
		token, err = tokens.Token(ctx)
		if err != nil || expired || token.Expiry.After(stale) {
			return token, err
		}
		timer.Reset(vertexTokenPollInterval)
	}
}
