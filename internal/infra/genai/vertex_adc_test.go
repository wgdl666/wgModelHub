package genai

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	genaisdk "google.golang.org/genai"
)

// 临时 RSA 凭据只用于本地 token 服务，测试不依赖开发者登录或线上 Google 账号。
func vertexServiceAccountADC(t *testing.T, tokenURL string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	data, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "credential-project",
		"client_email": "vertex@test-project.iam.gserviceaccount.com",
		"private_key":  string(keyPEM), "token_uri": tokenURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
}

type vertexADCTestTransport struct {
	testing *testing.T
	vertexRecordingTransport
	tokenCalls int
	lifetime   int
	tokenError bool
}

func (tr *vertexADCTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "oauth2.googleapis.com" {
		if req.URL.Host != "aiplatform.googleapis.com" || !strings.HasPrefix(req.URL.Path, "/v1beta1/projects/test-project/locations/global/") {
			tr.testing.Errorf("wrong Vertex endpoint: %s", req.URL)
		}
		if req.Header.Get("Authorization") != fmt.Sprintf("Bearer token-%d", tr.tokenCalls) || req.Header.Get("X-Goog-User-Project") != "test-project" {
			tr.testing.Error("ADC bearer or configured quota project missing")
		}
		if req.Header.Get("X-Goog-Api-Key") != "" || req.URL.Query().Get("key") != "" {
			tr.testing.Error("ADC must not send an API key")
		}
		return tr.vertexRecordingTransport.RoundTrip(req)
	}
	tr.tokenCalls++
	defer req.Body.Close()
	if err := req.ParseForm(); err != nil {
		return nil, err
	}
	assertion := strings.Split(req.Form.Get("assertion"), ".")
	if req.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || len(assertion) != 3 {
		tr.testing.Fatal("missing service-account JWT exchange")
	}
	payload, err := base64.RawURLEncoding.DecodeString(assertion[1])
	if err != nil || !strings.Contains(string(payload), "https://www.googleapis.com/auth/cloud-platform") {
		tr.testing.Error("cloud-platform scope missing")
	}
	status := http.StatusOK
	body := fmt.Sprintf(`{"access_token":"token-%d","token_type":"Bearer","expires_in":%d}`, tr.tokenCalls, tr.lifetime)
	if tr.tokenError {
		status, body = http.StatusBadRequest, `{"error":"invalid_grant","error_description":"private-credential-canary"}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func newVertexADCTestProvider(t *testing.T, lifetime int) (*Provider, *vertexADCTestTransport) {
	t.Helper()
	vertexServiceAccountADC(t, "https://oauth2.googleapis.com/token")
	// 残留的 API key/项目/区域环境变量不能改变显式选择的 ADC 身份和项目。
	t.Setenv("GOOGLE_API_KEY", "environment-key-must-not-be-sent")
	t.Setenv("GEMINI_API_KEY", "environment-gemini-key-must-not-be-sent")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "environment-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")
	transport := &vertexADCTestTransport{testing: t, lifetime: lifetime}
	p, err := newVertexAI(context.Background(), "vertex", "test-project", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return p, transport
}

func TestVertexADCTextStreamImageAndCacheShareToken(t *testing.T) {
	p, transport := newVertexADCTestProvider(t, 3600)
	event, err := p.Generate(context.Background(), models.Gemini25Flash, vertexTextRequest())
	if err != nil || event.GetItems()[0].GetText() != "ok" {
		t.Fatalf("text event=%v err=%v", event, err)
	}
	transport.response = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"streamed\"}]},\"finishReason\":\"STOP\"}]}\n\n"
	var streamed string
	final, err := p.GenerateStream(context.Background(), models.Gemini25Flash, vertexTextRequest(), func(event *modelhubv2.GenerateEvent) error {
		for _, item := range event.GetItems() {
			streamed += item.GetText()
		}
		return nil
	})
	if err != nil || streamed != "streamed" || !final.GetFinal() || len(final.GetItems()) != 0 {
		t.Fatalf("stream=%q final=%v err=%v", streamed, final, err)
	}
	if !strings.HasSuffix(transport.last.URL.Path, ":streamGenerateContent") {
		t.Fatalf("stream path=%s", transport.last.URL.Path)
	}
	out := onePixelPNG(t)
	transport.response = `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(out) + `"}}]}}]}`
	event, err = p.GenerateImage(context.Background(), models.GeminiNanoBanana21, vertexImageRequest("shirt"))
	if err != nil || len(event.GetItems()) != 1 || event.GetItems()[0].GetImage().GetMimeType() != "image/png" {
		t.Fatalf("image event=%v err=%v", event, err)
	}
	if !strings.HasSuffix(transport.last.URL.Path, "/models/"+models.GeminiNanoBanana21+":generateContent") {
		t.Fatalf("image path=%s", transport.last.URL.Path)
	}
	// 自动前缀缓存通过同一 SDK 客户端创建，复用已有 ADC token。
	transport.response = `{"name":"projects/test-project/locations/global/cachedContents/test-cache"}`
	cache, err := p.client.Caches.Create(context.Background(), models.Gemini25Flash, &genaisdk.CreateCachedContentConfig{
		TTL:               time.Hour,
		SystemInstruction: genaisdk.NewContentFromText("test system", genaisdk.RoleUser),
	})
	if err != nil || cache == nil || cache.Name == "" {
		t.Fatalf("cache=%v err=%v", cache, err)
	}
	if !strings.HasSuffix(transport.last.URL.Path, "/cachedContents") || transport.calls != 4 || transport.tokenCalls != 1 {
		t.Fatalf("models=%d tokens=%d", transport.calls, transport.tokenCalls)
	}
}

func TestVertexADCRefreshesExpiredToken(t *testing.T) {
	p, transport := newVertexADCTestProvider(t, 1)
	if _, err := p.Generate(context.Background(), models.Gemini25Flash, vertexTextRequest()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := p.Generate(context.Background(), models.Gemini25Flash, vertexTextRequest()); err != nil {
		t.Fatal(err)
	}
	if transport.tokenCalls != 2 || transport.calls != 2 {
		t.Fatalf("models=%d tokens=%d", transport.calls, transport.tokenCalls)
	}
}

func TestVertexADCErrorsDoNotLeakOrCallModel(t *testing.T) {
	p, transport := newVertexADCTestProvider(t, 3600)
	transport.tokenError = true
	_, err := p.Generate(context.Background(), models.Gemini25Flash, vertexTextRequest())
	if provider.Kind(err) != provider.ErrorConfiguration || !provider.IsNotAttempted(err) || strings.Contains(err.Error(), "private-credential-canary") {
		t.Fatalf("token error not sanitized/classified: %v", err)
	}
	if transport.calls != 0 {
		t.Fatal("failed token exchange must not call model or fall back to API key")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Generate(ctx, models.Gemini25Flash, vertexTextRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(path, []byte(`{"private_key":"private-credential-canary"`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	_, err = NewVertexAI(context.Background(), "vertex", "test-project", "")
	if provider.Kind(err) != provider.ErrorConfiguration || strings.Contains(err.Error(), "private-credential-canary") {
		t.Fatalf("invalid ADC not sanitized: %v", err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	if _, err := NewVertexAI(context.Background(), "vertex", "test-project", ""); provider.Kind(err) != provider.ErrorConfiguration {
		t.Fatalf("missing ADC must fail startup: %v", err)
	}
}

func TestVertexADCProxyCoversTokenAndModel(t *testing.T) {
	var tokenCalls int
	var connected string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Host == "token.fixture.invalid" {
			tokenCalls++
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"access_token":"proxy-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		if r.Method != http.MethodConnect {
			t.Error("unexpected proxy request")
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		connected = r.Host
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
			_ = conn.Close()
		}
	}))
	defer proxy.Close()
	vertexServiceAccountADC(t, "http://token.fixture.invalid/token")
	p, err := NewVertexAI(context.Background(), "vertex", "test-project", proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = p.Generate(ctx, models.Gemini25Flash, vertexTextRequest())
	if tokenCalls != 1 || connected != "aiplatform.googleapis.com:443" {
		t.Fatalf("token calls=%d model proxy=%q", tokenCalls, connected)
	}
}
