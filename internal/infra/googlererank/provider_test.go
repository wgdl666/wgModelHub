package googlererank

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
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
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/auth"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const testInput = `{"query":"白色通勤衬衫","documents":["黑裤子","白衬衫"],"instruct":"do not forward this"}`

func request(text string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Model: models.GoogleSemanticRankerDefault005,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role: modelhubv2.Role_ROLE_USER, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: text}}},
		}}}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}
}

// 用临时 RSA 服务账号和本地 token 服务验证真实 ADC 链路，不依赖开发者登录或线上密钥。
func serviceAccountADC(t *testing.T, tokenURL string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	data, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "test-project", "client_email": "rerank@test-project.iam.gserviceaccount.com",
		"private_key": string(keyPEM), "token_uri": tokenURL,
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

func TestADCRequestMappingAndTokenRefresh(t *testing.T) {
	var tokenCalls, rankCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || r.Form.Get("assertion") == "" {
				t.Error("missing service-account JWT exchange")
			}
			// 使用短有效期，第二次请求在真实过期后验证 SDK 自动刷新。
			fmt.Fprintf(w, `{"access_token":"token-%d","token_type":"Bearer","expires_in":1}`, tokenCalls.Add(1))
			return
		}
		call := rankCalls.Add(1)
		if r.URL.Path != "/v1/projects/test-project/locations/global/rankingConfigs/default_ranking_config:rank" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer token-%d", call) || r.Header.Get("X-Goog-User-Project") != "test-project" || r.Header.Get("X-Goog-Api-Key") != "" {
			t.Error("OAuth authorization or quota project missing; API key must not be used")
		}
		var payload struct {
			Model    string   `json:"model"`
			Query    string   `json:"query"`
			TopN     int      `json:"topN"`
			Records  []record `json:"records"`
			Ignore   bool     `json:"ignoreRecordDetailsInResponse"`
			Instruct any      `json:"instruct"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != models.GoogleSemanticRankerDefault005 || payload.Query != "白色通勤衬衫" || payload.TopN != 2 || !payload.Ignore || payload.Instruct != nil || len(payload.Records) != 2 {
			t.Errorf("unexpected ranking payload: %+v", payload)
		} else if payload.Records[0] != (record{ID: "0", Content: "黑裤子"}) || payload.Records[1] != (record{ID: "1", Content: "白衬衫"}) {
			t.Errorf("records lost original indices: %+v", payload.Records)
		}
		io.WriteString(w, `{"records":[{"id":"1","score":0.9},{"id":"0","score":0.1}]}`)
	}))
	t.Cleanup(server.Close)
	serviceAccountADC(t, server.URL+"/token")
	p, err := New("google", "test-project", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.endpoint != "https://discoveryengine.googleapis.com/v1/projects/test-project/locations/global/rankingConfigs/default_ranking_config:rank" {
		t.Fatalf("wrong endpoint: %s", p.endpoint)
	}
	p.endpoint = strings.Replace(p.endpoint, "https://discoveryengine.googleapis.com", server.URL, 1)
	for attempt := 0; attempt < 2; attempt++ {
		if attempt == 1 {
			time.Sleep(1100 * time.Millisecond)
		}
		event, err := p.Generate(context.Background(), models.GoogleSemanticRankerDefault005, request(testInput))
		if err != nil {
			t.Fatal(err)
		}
		if !event.Final || event.GetItems()[0].GetText() != `{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}` {
			t.Fatalf("wrong final: %v", event)
		}
	}
	if tokenCalls.Load() != 2 || rankCalls.Load() != 2 {
		t.Fatalf("refresh=%d rank=%d", tokenCalls.Load(), rankCalls.Load())
	}
}

type tokenFunc func(context.Context) (*auth.Token, error)

func (f tokenFunc) Token(ctx context.Context) (*auth.Token, error) { return f(ctx) }

func testProvider(t *testing.T, status int, body string) *Provider {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); io.WriteString(w, body) }))
	t.Cleanup(s.Close)
	return &Provider{name: "google", project: "test-project", endpoint: s.URL, client: s.Client(), tokens: tokenFunc(func(context.Context) (*auth.Token, error) { return &auth.Token{Value: "test-token"}, nil })}
}

func TestInvalidInputsDoNotCallGoogle(t *testing.T) {
	p := testProvider(t, 200, "")
	p.tokens = tokenFunc(func(context.Context) (*auth.Token, error) { t.Fatal("invalid input acquired a token"); return nil, nil })
	for _, body := range []string{"", `{}`, `{"query":"q","documents":[]}`, `{"query":"q","documents":[""]}`, `{"query":"q","documents":["a",1]}`} {
		_, err := p.Generate(context.Background(), models.GoogleSemanticRankerDefault005, request(body))
		if provider.Kind(err) != provider.ErrorInvalidArgument || !provider.IsNotAttempted(err) {
			t.Errorf("input=%s err=%v", body, err)
		}
	}
	documents := make([]string, 1001)
	for i := range documents {
		documents[i] = "document"
	}
	raw, _ := json.Marshal(rerankInput{Query: "q", Documents: documents})
	if _, err := p.Generate(context.Background(), models.GoogleSemanticRankerDefault005, request(string(raw))); !provider.IsNotAttempted(err) {
		t.Fatalf("record limit: %v", err)
	}
}

func TestInvalidResultsCannotMisassociateCandidates(t *testing.T) {
	for _, body := range []string{
		`not-json`, `{"records":[]}`, `{"records":[{"id":"0","score":0.5}]}`,
		`{"records":[{"id":"0","score":0.5},{"id":"0","score":0.1}]}`,
		`{"records":[{"id":"2","score":0.5},{"id":"0","score":0.1}]}`,
		`{"records":[{"id":"01","score":0.5},{"id":"0","score":0.1}]}`,
		`{"records":[{"id":"1"},{"id":"0","score":0.1}]}`,
		`{"records":[{"id":"1","score":1.1},{"id":"0","score":0.1}]}`,
	} {
		if _, err := decodeResults([]byte(body), 2); provider.Kind(err) != provider.ErrorInvalidResponse {
			t.Errorf("body=%s err=%v", body, err)
		}
	}
	if _, err := decodeResults([]byte(`{"records":[{"id":"0","score":0}]}`), 1); err != nil {
		t.Fatalf("zero is a valid score: %v", err)
	}
}

func TestHTTPErrorClassificationAndRedaction(t *testing.T) {
	for code, kind := range map[int]provider.ErrorKind{400: provider.ErrorInvalidArgument, 401: provider.ErrorConfiguration, 403: provider.ErrorConfiguration, 429: provider.ErrorRateLimited, 500: provider.ErrorUnavailable, 504: provider.ErrorTimeout} {
		p := testProvider(t, code, `{"error":"secret prompt and token"}`)
		_, err := p.Generate(context.Background(), models.GoogleSemanticRankerDefault005, request(testInput))
		if provider.Kind(err) != kind || strings.Contains(err.Error(), "secret") || provider.IsNotAttempted(err) {
			t.Errorf("status=%d err=%v", code, err)
		}
	}
}

func TestTokenFailureAndCancellation(t *testing.T) {
	p := testProvider(t, 200, "")
	p.tokens = tokenFunc(func(context.Context) (*auth.Token, error) { return nil, errors.New("secret credentials") })
	_, err := p.Generate(context.Background(), models.GoogleSemanticRankerDefault005, request(testInput))
	if provider.Kind(err) != provider.ErrorConfiguration || !provider.IsNotAttempted(err) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	p.tokens = tokenFunc(func(ctx context.Context) (*auth.Token, error) { return nil, ctx.Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Generate(ctx, models.GoogleSemanticRankerDefault005, request(testInput)); !errors.Is(err, context.Canceled) || !provider.IsNotAttempted(err) {
		t.Fatal(err)
	}
}

func TestStreamingHasOneTextDeltaAndMetadataFinal(t *testing.T) {
	p := testProvider(t, 200, `{"records":[{"id":"1","score":0.9},{"id":"0","score":0.1}]}`)
	var deltas []*modelhubv2.GenerateEvent
	final, err := p.GenerateStream(context.Background(), models.GoogleSemanticRankerDefault005, request(testInput), func(e *modelhubv2.GenerateEvent) error { deltas = append(deltas, e); return nil })
	if err != nil || !final.Final || len(final.Items) != 0 || len(deltas) != 1 || deltas[0].Final || len(deltas[0].Items) != 1 {
		t.Fatalf("deltas=%v final=%v err=%v", deltas, final, err)
	}
	stopped := errors.New("consumer stopped")
	if _, err := p.GenerateStream(context.Background(), models.GoogleSemanticRankerDefault005, request(testInput), func(*modelhubv2.GenerateEvent) error { return stopped }); !errors.Is(err, stopped) {
		t.Fatal(err)
	}
}

func TestNewRejectsUnavailableADCWithoutLeakingContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"type":"secret-credential"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	if _, err := New("google", "test-project", ""); provider.Kind(err) != provider.ErrorConfiguration || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestADCUsesConfiguredProxyForTokensAndRanking(t *testing.T) {
	var tokens, ranks atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Host {
		case "oauth.invalid":
			tokens.Add(1)
			io.WriteString(w, `{"access_token":"proxy-token","token_type":"Bearer","expires_in":3600}`)
		case "ranking.invalid":
			ranks.Add(1)
			if r.Header.Get("Authorization") != "Bearer proxy-token" {
				t.Error("missing proxy-sourced OAuth token")
			}
			io.WriteString(w, `{"records":[{"id":"1","score":0.9},{"id":"0","score":0.1}]}`)
		default:
			t.Errorf("unexpected proxy host %s", r.URL.Host)
		}
	}))
	t.Cleanup(proxy.Close)
	serviceAccountADC(t, "http://oauth.invalid/token")
	p, err := New("google", "test-project", proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.endpoint = "http://ranking.invalid/rank"
	for range 2 {
		if _, err := p.Generate(context.Background(), models.GoogleSemanticRankerDefault005, request(testInput)); err != nil {
			t.Fatal(err)
		}
	}
	if tokens.Load() != 1 || ranks.Load() != 2 {
		t.Fatalf("token cache/proxy: tokens=%d ranks=%d", tokens.Load(), ranks.Load())
	}
}
