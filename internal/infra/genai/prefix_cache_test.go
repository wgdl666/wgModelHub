package genai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/auth"
	"github.com/wgdl666/wgModelHub/internal/prefixcache"
	"github.com/wgdl666/wgModelHub/models"
	genaisdk "google.golang.org/genai"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// The same registry/locks can be shared by independent managers in tests.
type prefixMemoryStore struct {
	mu               sync.Mutex
	entries          map[string]*prefixcache.Entry
	locked           map[int64]bool
	puts             chan *prefixcache.Entry
	readError        error
	invalidateError  error
	getCalls         int
	beforeInvalidate func(context.Context) error
}

func newPrefixMemoryStore() *prefixMemoryStore {
	return &prefixMemoryStore{entries: make(map[string]*prefixcache.Entry), locked: make(map[int64]bool), puts: make(chan *prefixcache.Entry, 16)}
}

func (s *prefixMemoryStore) Get(_ context.Context, key string) (*prefixcache.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	if s.readError != nil {
		return nil, s.readError
	}
	if entry := s.entries[key]; entry != nil {
		copy := *entry
		return &copy, nil
	}
	return nil, nil
}

func (s *prefixMemoryStore) Put(_ context.Context, entry *prefixcache.Entry) error {
	s.mu.Lock()
	copy := *entry
	s.entries[entry.Key] = &copy
	s.mu.Unlock()
	s.puts <- &copy
	return nil
}

func (s *prefixMemoryStore) Invalidate(ctx context.Context, key, name string) error {
	if s.beforeInvalidate != nil {
		if err := s.beforeInvalidate(ctx); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalidateError != nil {
		return s.invalidateError
	}
	if old := s.entries[key]; old != nil && old.Name == name {
		delete(s.entries, key)
	}
	return nil
}

func (s *prefixMemoryStore) WithLock(_ context.Context, key int64, fn func(prefixcache.Store) error) (bool, error) {
	s.mu.Lock()
	if s.locked[key] {
		s.mu.Unlock()
		return false, nil
	}
	s.locked[key] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.locked, key)
		s.mu.Unlock()
	}()
	return true, fn(s)
}

func prefixRequest() *modelhubv2.GenerateRequest {
	zero := 0.0
	return &modelhubv2.GenerateRequest{
		Model: models.Gemini38Flash,
		Input: &modelhubv2.Input{
			Items: []*modelhubv2.InputItem{
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_SYSTEM, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "Fixed wardrobe rules"}}}}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_USER, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "Find a black dress"}}}}}},
			},
			Tools:      []*modelhubv2.Tool{{Function: &modelhubv2.FunctionDefinition{Name: "closet_search", ParametersJsonSchema: []byte(`{"type":"object","properties":{"color":{"type":"string"}},"required":["color"]}`)}}},
			ToolChoice: &modelhubv2.ToolChoice{Mode: modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{Temperature: &zero}}},
	}
}

func prefixTestProvider(t *testing.T, handler http.HandlerFunc) (*Provider, *prefixcache.Manager, *prefixMemoryStore) {
	t.Helper()
	return prefixTestProviderBackend(t, genaisdk.BackendGeminiAPI, handler)
}

func prefixTestProviderBackend(t *testing.T, backend genaisdk.Backend, handler http.HandlerFunc) (*Provider, *prefixcache.Manager, *prefixMemoryStore) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	var p *Provider
	var err error
	if backend == genaisdk.BackendVertexAI {
		vertexServiceAccountADC(t, "https://oauth2.googleapis.com/token")
		transport := &countingTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Host == "oauth2.googleapis.com" {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
				return
			}
			handler(w, r)
		})}
		p, err = newVertexAI(context.Background(), "vertex-test", "test-project", &http.Client{Transport: transport})
	} else {
		p, err = NewGemini(context.Background(), "developer-test", geminiTestAPIKey, server.URL, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	store := newPrefixMemoryStore()
	manager := prefixcache.New(context.Background(), store)
	t.Cleanup(manager.Close)
	p.ConfigurePrefixCache(manager, func(string) bool { return true })
	return p, manager, store
}

func decodePrefixBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	return body
}

func writePrefixResponse(w http.ResponseWriter, cached bool) {
	w.Header().Set("Content-Type", "application/json")
	count := 0
	if cached {
		count = 5
	}
	fmt.Fprintf(w, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"closet_search","args":{"color":"black"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":%d,"candidatesTokenCount":2,"totalTokenCount":12}}`, count)
}

func awaitPrefixPut(t *testing.T, store *prefixMemoryStore) *prefixcache.Entry {
	t.Helper()
	select {
	case entry := <-store.puts:
		return entry
	case <-time.After(3 * time.Second):
		t.Fatal("background cache was not registered")
		return nil
	}
}

func TestManagedPrefixColdCallDoesNotWaitAndDynamicInputReusesCache(t *testing.T) {
	creating := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var cacheCalls atomic.Int32
	requests := make(chan map[string]any, 4)
	p, _, store := prefixTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodePrefixBody(t, r)
		if strings.HasSuffix(r.URL.Path, "/cachedContents") {
			cacheCalls.Add(1)
			if body["ttl"] != "3600s" || body["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)["mode"] != "ANY" {
				t.Errorf("cached prefix lost TTL or required tool mode: %v", body)
			}
			close(creating)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"name":"cachedContents/shared","expireTime":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
			return
		}
		requests <- body
		writePrefixResponse(w, body["cachedContent"] != nil)
	})
	req := prefixRequest()
	original := proto.Clone(req)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	first, err := p.Generate(ctx, req.Model, req)
	if err != nil || first.GetUsage().GetCachedTokens() != 0 {
		t.Fatalf("cold call waited/failed: final=%v err=%v", first, err)
	}
	select {
	case <-creating:
	case <-ctx.Done():
		t.Fatal("cache creation not scheduled")
	}
	full := <-requests
	if full["systemInstruction"] == nil || full["tools"] == nil || full["cachedContent"] != nil {
		t.Fatalf("cold request lost full prefix: %v", full)
	}
	once.Do(func() { close(release) })
	awaitPrefixPut(t, store)
	next := proto.Clone(req).(*modelhubv2.GenerateRequest)
	next.Input.Items[1].GetMessage().Parts[0] = &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Text{Text: "Find a white shirt instead"}}
	final, err := p.Generate(ctx, next.Model, next)
	if err != nil || final.GetUsage().GetCachedTokens() != 5 {
		t.Fatalf("cache reuse failed: final=%v err=%v", final, err)
	}
	used := <-requests
	if used["cachedContent"] != "cachedContents/shared" || used["systemInstruction"] != nil || used["tools"] != nil || used["toolConfig"] != nil {
		t.Fatalf("cache reference repeats static fields: %v", used)
	}
	if !strings.Contains(fmt.Sprint(used["contents"]), "white shirt") || cacheCalls.Load() != 1 || !proto.Equal(original, req) {
		t.Fatal("dynamic contents changed, prefix recreated, or original request mutated")
	}
}

func TestManagedPrefixKeyUsesStaticFieldsAndIsolation(t *testing.T) {
	t.Setenv("XX_WG_REGION", "CN")
	p := &Provider{cacheIdentity: cacheIdentity("developer", "credential-1")}
	r := prefixRequest()
	base := p.buildConfig(r.Model, r)
	key := p.prefixKey(context.Background(), r.Model, base)
	changed := proto.Clone(r).(*modelhubv2.GenerateRequest)
	changed.Input.Items[1].GetMessage().Parts[0] = &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Text{Text: "different dynamic contents"}}
	changed.Output.GetText().ResponseFormat = &modelhubv2.ResponseFormat{Type: modelhubv2.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_SCHEMA, JsonSchema: []byte(`{"type":"object"}`)}
	if got := p.prefixKey(context.Background(), r.Model, p.buildConfig(r.Model, changed)); got != key {
		t.Fatal("dynamic/output fields must not invalidate the static prefix")
	}
	for _, change := range []func(*modelhubv2.GenerateRequest){
		func(r *modelhubv2.GenerateRequest) {
			r.Input.Items[0].GetMessage().Parts[0] = &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Text{Text: "Fixed wardrobe ruleS"}}
		},
		func(r *modelhubv2.GenerateRequest) { r.Input.Tools[0].Function.Description = "changed tool" },
		func(r *modelhubv2.GenerateRequest) {
			r.Input.ToolChoice.Mode = modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_AUTO
		},
	} {
		changed := proto.Clone(r).(*modelhubv2.GenerateRequest)
		change(changed)
		if got := p.prefixKey(context.Background(), r.Model, p.buildConfig(r.Model, changed)); got == key {
			t.Fatal("static change reused the old key")
		}
	}
	other := &Provider{cacheIdentity: cacheIdentity("vertex", "project-1")}
	if got := other.prefixKey(context.Background(), r.Model, base); got == key {
		t.Fatal("cross-provider cache key collision")
	}
	if got := p.prefixKey(context.Background(), models.Gemini37Flash, base); got == key {
		t.Fatal("cross-model cache key collision")
	}
	caller := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-wg-caller-service", "another-caller"))
	if got := p.prefixKey(caller, r.Model, base); got == key {
		t.Fatal("cross-caller cache key collision")
	}
	public := auth.ContextWithPublicPrincipal(caller, "principal-1", "key-1")
	publicKey := p.prefixKey(public, r.Model, base)
	rotated := auth.ContextWithPublicPrincipal(caller, "principal-1", "key-2")
	if got := p.prefixKey(rotated, r.Model, base); got != publicKey {
		t.Fatal("public client key rotation must preserve the same principal scope")
	}
	otherPublic := auth.ContextWithPublicPrincipal(caller, "principal-2", "key-1")
	if got := p.prefixKey(otherPublic, r.Model, base); got == publicKey {
		t.Fatal("cross-principal cache key collision")
	}
	t.Setenv("XX_WG_REGION", "SG")
	if got := p.prefixKey(context.Background(), r.Model, base); got == key {
		t.Fatal("cross-region cache key collision")
	}
}

func TestManagedPrefixCacheErrorsFallBackOnce(t *testing.T) {
	for _, tc := range []struct {
		backend         genaisdk.Backend
		code            int
		status, message string
	}{
		{genaisdk.BackendVertexAI, 400, "INVALID_ARGUMENT", "Cache content 918477188586536960 is expired."},
		{genaisdk.BackendGeminiAPI, 403, "PERMISSION_DENIED", "CachedContent not found (or permission denied)"},
		{genaisdk.BackendGeminiAPI, 404, "NOT_FOUND", "Not found: cached content metadata for missing."},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%d/stream=%v", tc.backend, tc.code, stream), func(t *testing.T) {
				var calls atomic.Int32
				p, _, store := prefixTestProviderBackend(t, tc.backend, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost {
						t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
					}
					body := decodePrefixBody(t, r)
					if strings.HasSuffix(r.URL.Path, "/cachedContents") {
						w.WriteHeader(429)
						fmt.Fprint(w, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"temporary"}}`)
						return
					}
					calls.Add(1)
					if body["cachedContent"] != nil {
						w.WriteHeader(tc.code)
						json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": tc.code, "status": tc.status, "message": tc.message}})
						return
					}
					if body["systemInstruction"] == nil || body["tools"] == nil || body["toolConfig"] == nil {
						t.Error("fallback lost original system/tools/mode")
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"closet_search\",\"args\":{\"color\":\"black\"}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"totalTokenCount\":12}}\n\n")
						return
					}
					writePrefixResponse(w, false)
				})
				req := prefixRequest()
				key := p.prefixKey(context.Background(), req.Model, p.buildConfig(req.Model, req))
				store.Put(context.Background(), &prefixcache.Entry{Key: key, Name: "cachedContents/missing", ExpiresAt: time.Now().Add(time.Hour)})
				var err error
				if stream {
					var emitted int
					_, err = p.GenerateStream(context.Background(), req.Model, req, func(e *modelhubv2.GenerateEvent) error { emitted++; return nil })
					if emitted != 1 {
						t.Fatalf("unexpected duplicate/missing output: %d", emitted)
					}
				} else {
					_, err = p.Generate(context.Background(), req.Model, req)
				}
				if err != nil || calls.Load() != 2 {
					t.Fatalf("expected one full fallback: calls=%d err=%v", calls.Load(), err)
				}
				waitForPrefixCondition(t, func() bool {
					got, _ := store.Get(context.Background(), key)
					return got == nil
				})
			})
		}
	}
}

func TestManagedPrefixCacheErrorRetryRequiresManagedCacheAndStopsAfterOnce(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("managed=%v/stream=%v", managed, stream), func(t *testing.T) {
				var calls atomic.Int32
				p, _, store := prefixTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/cachedContents") {
						w.WriteHeader(429)
						fmt.Fprint(w, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"temporary"}}`)
						return
					}
					calls.Add(1)
					w.WriteHeader(403)
					fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"CachedContent not found (or permission denied)"}}`)
				})
				p.cacheEnabled = func(string) bool { return managed }
				req := prefixRequest()
				key := p.prefixKey(context.Background(), req.Model, p.buildConfig(req.Model, req))
				store.Put(context.Background(), &prefixcache.Entry{Key: key, Name: "cachedContents/rejected", ExpiresAt: time.Now().Add(time.Hour)})
				var err error
				if stream {
					_, err = p.GenerateStream(context.Background(), req.Model, req, func(*modelhubv2.GenerateEvent) error {
						t.Error("failed attempts must not emit output")
						return nil
					})
				} else {
					_, err = p.Generate(context.Background(), req.Model, req)
				}
				wantCalls := int32(1)
				if managed {
					wantCalls = 2
				}
				if err == nil || calls.Load() != wantCalls {
					t.Fatalf("calls=%d want=%d err=%v", calls.Load(), wantCalls, err)
				}
			})
		}
	}
}

func TestManagedPrefixRecoveryDoesNotWaitForMaintenance(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			invalidating, creating := make(chan struct{}), make(chan struct{})
			releaseInvalidation, releaseCreation := make(chan struct{}), make(chan struct{})
			var releaseInvalidationOnce, releaseCreationOnce sync.Once
			defer releaseInvalidationOnce.Do(func() { close(releaseInvalidation) })
			defer releaseCreationOnce.Do(func() { close(releaseCreation) })
			var calls atomic.Int32
			p, _, store := prefixTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
				}
				body := decodePrefixBody(t, r)
				if strings.HasSuffix(r.URL.Path, "/cachedContents") {
					close(creating)
					select {
					case <-releaseCreation:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"name":"cachedContents/rebuilt","expireTime":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
					return
				}
				calls.Add(1)
				if body["cachedContent"] != nil {
					w.WriteHeader(403)
					fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"CachedContent not found (or permission denied)"}}`)
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
					return
				}
				writePrefixResponse(w, false)
			})
			store.beforeInvalidate = func(ctx context.Context) error {
				close(invalidating)
				select {
				case <-releaseInvalidation:
					return nil
				case <-ctx.Done():
					t.Errorf("invalidation timed out before the full-input retry completed: %v", ctx.Err())
					return ctx.Err()
				}
			}
			req := prefixRequest()
			key := p.prefixKey(context.Background(), req.Model, p.buildConfig(req.Model, req))
			store.Put(context.Background(), &prefixcache.Entry{Key: key, Name: "cachedContents/rejected", ExpiresAt: time.Now().Add(time.Hour)})
			awaitPrefixPut(t, store) // Discard the seed entry; the next put must be the rebuild.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				if stream {
					_, err = p.GenerateStream(ctx, req.Model, req, func(*modelhubv2.GenerateEvent) error { return nil })
				} else {
					_, err = p.Generate(ctx, req.Model, req)
				}
				done <- err
			}()
			select {
			case <-invalidating:
			case <-ctx.Done():
				t.Fatal("background invalidation was not scheduled")
			}
			select {
			case err := <-done:
				if err != nil || calls.Load() != 2 {
					t.Fatalf("full-input retry failed: calls=%d err=%v", calls.Load(), err)
				}
			case <-ctx.Done():
				t.Fatal("full-input retry waited for maintenance")
			}
			// The request is already done; its cancellation must not cancel maintenance.
			cancel()
			releaseInvalidationOnce.Do(func() { close(releaseInvalidation) })
			select {
			case <-creating:
			case <-time.After(2 * time.Second):
				t.Fatal("request cancellation prevented background cache creation")
			}
			releaseCreationOnce.Do(func() { close(releaseCreation) })
			if entry := awaitPrefixPut(t, store); entry.Name != "cachedContents/rebuilt" {
				t.Fatalf("replacement was not registered: %+v", entry)
			}
		})
	}
}

func TestManagedPrefixInvalidationFailureRetriesOnNextRequest(t *testing.T) {
	var calls atomic.Int32
	p, _, store := prefixTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodePrefixBody(t, r)
		if strings.HasSuffix(r.URL.Path, "/cachedContents") {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"temporary"}}`)
			return
		}
		calls.Add(1)
		if body["cachedContent"] != nil {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"cached content not found"}}`)
			return
		}
		writePrefixResponse(w, false)
	})
	req := prefixRequest()
	key := p.prefixKey(context.Background(), req.Model, p.buildConfig(req.Model, req))
	store.Put(context.Background(), &prefixcache.Entry{Key: key, Name: "cachedContents/missing", ExpiresAt: time.Now().Add(time.Hour)})
	store.invalidateError = errors.New("registry unavailable")
	if _, err := p.Generate(context.Background(), req.Model, req); err != nil {
		t.Fatal(err)
	}
	waitForPrefixCondition(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.getCalls >= 2 && len(store.locked) == 0
	})
	if entry, _ := store.Get(context.Background(), key); entry == nil {
		t.Fatal("failed invalidation unexpectedly removed the record")
	}
	store.mu.Lock()
	store.invalidateError = nil
	store.mu.Unlock()
	if _, err := p.Generate(context.Background(), req.Model, req); err != nil {
		t.Fatal(err)
	}
	waitForPrefixCondition(t, func() bool {
		entry, _ := store.Get(context.Background(), key)
		return entry == nil
	})
	if calls.Load() != 4 {
		t.Fatalf("generation calls=%d want cached attempt plus full input on each request", calls.Load())
	}
}

func TestManagedPrefixDoesNotReplayAfterStreamOutput(t *testing.T) {
	var calls atomic.Int32
	p, _, store := prefixTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"already sent\"}]}}]}\n\n")
		// Google's SDK recognizes a terminal bare JSON error line in this stream.
		fmt.Fprint(w, "{\"error\":{\"code\":404,\"status\":\"NOT_FOUND\",\"message\":\"cached content expired\"}}\n\n")
	})
	req := prefixRequest()
	key := p.prefixKey(context.Background(), req.Model, p.buildConfig(req.Model, req))
	store.Put(context.Background(), &prefixcache.Entry{Key: key, Name: "cachedContents/partial", ExpiresAt: time.Now().Add(time.Hour)})
	var emitted int
	_, err := p.GenerateStream(context.Background(), req.Model, req, func(*modelhubv2.GenerateEvent) error { emitted++; return nil })
	if err == nil || calls.Load() != 1 || emitted != 1 {
		t.Fatalf("partial stream replayed: calls=%d emitted=%d err=%v", calls.Load(), emitted, err)
	}
}

func TestCachedContentErrorsAreNarrowlyClassified(t *testing.T) {
	for _, tc := range []struct {
		code    int
		message string
		want    bool
	}{
		{404, "Not found: cached content metadata for 123", true},
		{400, "Cache content 918477188586536960 is expired.", true},
		{403, "CachedContent not found (or permission denied)", true},
		{400, "CACHED CONTENT cannot be used", true},
		{400, "CacheContent is invalid", true},
		{403, "cache_content is unavailable", true},
		{404, "cached_content metadata missing", true},
		{404, "model not found", false},
		{403, "Permission denied for model", false},
		{400, "cache is invalid", false},
		{429, "cached content rate limited", false},
		{500, "cached content unavailable", false},
		{400, "invalid response schema", false},
	} {
		if got := cachedContentError(fmt.Errorf("SDK request: %w", genaisdk.APIError{Code: tc.code, Message: tc.message})); got != tc.want {
			t.Errorf("code=%d message=%s classified=%v", tc.code, tc.message, got)
		}
	}
	if cachedContentError(nil) || cachedContentError(errors.New("cached content expired")) {
		t.Fatal("only SDK API errors should trigger cache recovery")
	}
}

func waitForPrefixCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("background operation did not complete")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagedPrefixCreationErrorsRetryOnLaterRequests(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(fmt.Sprint("unsupported=", unsupported), func(t *testing.T) {
			var cacheCalls atomic.Int32
			p, _, store := prefixTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				decodePrefixBody(t, r)
				if strings.HasSuffix(r.URL.Path, "/cachedContents") {
					if cacheCalls.Add(1) > 1 {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprintf(w, `{"name":"cachedContents/retried","expireTime":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
						return
					}
					message := "Invalid cache configuration"
					if unsupported {
						message = "Explicit caching is not supported for this model"
					}
					w.WriteHeader(400)
					json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 400, "status": "INVALID_ARGUMENT", "message": message}})
					return
				}
				writePrefixResponse(w, false)
			})
			req := prefixRequest()
			if _, err := p.Generate(context.Background(), req.Model, req); err != nil {
				t.Fatal(err)
			}
			waitForPrefixCondition(t, func() bool {
				if _, err := p.Generate(context.Background(), req.Model, req); err != nil {
					t.Fatal(err)
				}
				return cacheCalls.Load() >= 2
			})
			if entry := awaitPrefixPut(t, store); entry.Name != "cachedContents/retried" {
				t.Fatalf("retried cache was not registered: %+v", entry)
			}
			if cacheCalls.Load() != 2 {
				t.Fatalf("cache creation calls=%d want one failure and one success", cacheCalls.Load())
			}
		})
	}
}

func TestManagedPrefixBypassesDisabledRequests(t *testing.T) {
	var cacheCalls atomic.Int32
	p, _, _ := prefixTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodePrefixBody(t, r)
		if strings.HasSuffix(r.URL.Path, "/cachedContents") {
			cacheCalls.Add(1)
		}
		writePrefixResponse(w, body["cachedContent"] != nil)
	})
	req := prefixRequest()
	p.cacheEnabled = func(string) bool { return false }
	if _, err := p.Generate(context.Background(), req.Model, req); err != nil {
		t.Fatal(err)
	}
	p.cacheEnabled = func(string) bool { return true }
	req.Input.Caching = &modelhubv2.CachingConfig{Enabled: false}
	if _, err := p.Generate(context.Background(), req.Model, req); err != nil {
		t.Fatal(err)
	}
	if cacheCalls.Load() != 0 {
		t.Fatal("disabled request scheduled managed cache creation")
	}
}

func TestManagedPrefixRegistryFailureStillGeneratesFullRequest(t *testing.T) {
	var fullCalls atomic.Int32
	p, _, store := prefixTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodePrefixBody(t, r)
		if strings.HasSuffix(r.URL.Path, "/cachedContents") {
			t.Error("cache creation must not run after registry lookup fails inside the lock")
			return
		}
		if body["cachedContent"] != nil || body["systemInstruction"] == nil {
			t.Error("registry outage lost the full prompt")
		}
		fullCalls.Add(1)
		writePrefixResponse(w, false)
	})
	store.readError = context.DeadlineExceeded
	req := prefixRequest()
	if _, err := p.Generate(context.Background(), req.Model, req); err != nil || fullCalls.Load() != 1 {
		t.Fatalf("registry failure blocked generation: calls=%d err=%v", fullCalls.Load(), err)
	}
}

func TestManagedPrefixPreservesPlanningJSONSchema(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"outfits":{"type":"array","items":{"type":"string"}}},"required":["outfits"]}`)
	p, _, store := prefixTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodePrefixBody(t, r)
		generation := body["generationConfig"].(map[string]any)
		if body["cachedContent"] != "cachedContents/planning" || generation["responseMimeType"] != "application/json" || generation["responseJsonSchema"] == nil || generation["temperature"] != float64(0) {
			t.Errorf("planning generation configuration not preserved: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"{\"outfits\":[]}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":5}}`)
	})
	req := prefixRequest()
	req.Input.Tools, req.Input.ToolChoice = nil, nil
	req.Output.GetText().ResponseFormat = &modelhubv2.ResponseFormat{Type: modelhubv2.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_SCHEMA, JsonSchema: schema}
	key := p.prefixKey(context.Background(), req.Model, p.buildConfig(req.Model, req))
	store.Put(context.Background(), &prefixcache.Entry{Key: key, Name: "cachedContents/planning", ExpiresAt: time.Now().Add(time.Hour)})
	final, err := p.Generate(context.Background(), req.Model, req)
	if err != nil || len(final.GetItems()) != 1 || final.GetItems()[0].GetText() != `{"outfits":[]}` {
		t.Fatalf("planning response changed: final=%v err=%v", final, err)
	}
}

func TestManagedShortPrefixSuppressesCreationAndPreservesGeneration(t *testing.T) {
	for _, message := range []string{
		"The cached content is of 21 tokens. The minimum token count to start explicit caching is 4096.",
		"Cached content is too small. min_total_token_count=4096",
	} {
		t.Run(message, func(t *testing.T) {
			var cacheCalls atomic.Int32
			p, manager, _ := prefixTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				body := decodePrefixBody(t, r)
				if strings.HasSuffix(r.URL.Path, "/cachedContents") {
					cacheCalls.Add(1)
					w.WriteHeader(400)
					json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 400, "status": "INVALID_ARGUMENT", "message": message}})
					return
				}
				if body["cachedContent"] != nil || body["systemInstruction"] == nil || body["tools"] == nil {
					t.Error("short prefix rejection must preserve full generation input")
				}
				writePrefixResponse(w, false)
			})
			req := prefixRequest()
			key := p.prefixKey(context.Background(), req.Model, p.buildConfig(req.Model, req))
			if _, err := p.Generate(context.Background(), req.Model, req); err != nil {
				t.Fatal(err)
			}
			waitForPrefixCondition(t, func() bool { return manager.Suppressed(key) })
			for i := 0; i < 5; i++ {
				if _, err := p.Generate(context.Background(), req.Model, req); err != nil {
					t.Fatal(err)
				}
			}
			manager.Close()
			if cacheCalls.Load() != 1 {
				t.Fatalf("cache calls=%d want 1", cacheCalls.Load())
			}
		})
	}
}

func TestInsufficientCachedContentTokensDoesNotSuppressOtherErrors(t *testing.T) {
	for _, err := range []error{
		genaisdk.APIError{Code: 504, Message: "Deadline expired before operation could complete."},
		genaisdk.APIError{Code: 403, Message: "The cached content minimum token count to start explicit caching"},
		genaisdk.APIError{Code: 400, Message: "Invalid cache configuration"},
		genaisdk.APIError{Code: 400, Message: "Request too small"},
	} {
		if insufficientCachedContentTokens(err) {
			t.Errorf("unrelated error classified as short prefix: %v", err)
		}
	}
}
