package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/models"
)

func setValidAppConfigEnv(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("XX_WG_SERVICE_NAME", "modelhub")
	t.Setenv("XX_WG_ENV", "dev")
	t.Setenv("XX_WG_REGION", "SG")
	t.Setenv("AWS_APPCONFIG_AGENT_ENDPOINT", endpoint)
}

func TestNewAppConfigLoaderRejectsIdentityMismatch(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "service", key: "XX_WG_SERVICE_NAME", value: "wghub"},
		{name: "environment", key: "XX_WG_ENV", value: "staging"},
		{name: "region", key: "XX_WG_REGION", value: "CN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setValidAppConfigEnv(t, "http://127.0.0.1:2772")
			t.Setenv(tt.key, tt.value)
			if _, err := NewAppConfigLoaderFromEnv(); err == nil {
				t.Fatalf("expected %s mismatch error, got nil", tt.key)
			}
		})
	}
}

func TestNewAppConfigLoaderRejectsUnsafeEndpoint(t *testing.T) {
	tests := []string{
		"https://127.0.0.1:2772",
		"http://192.0.2.10:2772",
		"http://user:pass@127.0.0.1:2772",
		"http://127.0.0.1:2772?token=secret",
		"http://127.0.0.1:2772#fragment",
	}
	for _, endpoint := range tests {
		t.Run(endpoint, func(t *testing.T) {
			setValidAppConfigEnv(t, endpoint)
			if _, err := NewAppConfigLoaderFromEnv(); err == nil {
				t.Fatalf("expected endpoint %q to be rejected", endpoint)
			}
		})
	}
}

func TestAppConfigLoaderLoadsValidatedYAML(t *testing.T) {
	want := validConfig()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method=%s, want GET", r.Method)
		}
		if r.URL.Path != "/applications/modelhub/environments/dev/configurations/config-dev" {
			t.Errorf("path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte(mustYAML(t, want)))
	}))
	defer server.Close()
	setValidAppConfigEnv(t, server.URL)

	loader, err := NewAppConfigLoaderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	got, raw, err := loader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("loaded config is invalid: %v", err)
	}
	if strings.TrimSpace(raw) == "" {
		t.Fatal("raw configuration is empty")
	}
}

func TestAppConfigLoaderRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
	}{
		{
			name: "redirect",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://127.0.0.1:1/secret", http.StatusFound)
			}),
		},
		{
			name: "server error",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "sensitive response body", http.StatusInternalServerError)
			}),
		},
		{
			name: "empty",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
		},
		{
			name: "oversize",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat("x", (256<<10)+1)))
			}),
		},
		{
			name: "invalid yaml",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("providers: credential-marker-that-must-not-leak\n"))
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()
			setValidAppConfigEnv(t, server.URL)

			loader, err := NewAppConfigLoaderFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := loader.Load(context.Background()); err == nil {
				t.Fatalf("expected %s response to fail", tt.name)
			} else if strings.Contains(err.Error(), "sensitive response body") ||
				strings.Contains(err.Error(), "credential-marker-that-must-not-leak") {
				t.Fatalf("configuration content leaked in error: %v", err)
			}
		})
	}
}

type appConfigAgentStub struct {
	mu      sync.Mutex
	version string
	body    string
}

func (s *appConfigAgentStub) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.version != "" {
		w.Header().Set("Configuration-Version", s.version)
	}
	_, _ = w.Write([]byte(s.body))
}

func (s *appConfigAgentStub) set(version, body string) {
	s.mu.Lock()
	s.version = version
	s.body = body
	s.mu.Unlock()
}

func newTestAppConfigLoader(t *testing.T, stub *appConfigAgentStub) *AppConfigLoader {
	t.Helper()
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	setValidAppConfigEnv(t, server.URL)
	loader, err := NewAppConfigLoaderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	loader.pollInterval = 20 * time.Millisecond
	t.Cleanup(loader.Close)
	return loader
}

func waitForAppConfigCallback(t *testing.T, got *[]string, mu *sync.Mutex, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, item := range *got {
			if item == want {
				mu.Unlock()
				return
			}
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for AppConfig callback %q", want)
}

func TestAppConfigListenAppliesBodyChange(t *testing.T) {
	initial := mustYAML(t, validConfig())
	nextCfg := validConfig()
	gemini := nextCfg.Providers["gemini"]
	gemini.Models = []string{models.Gemini25Flash, models.Gemini25FlashImage, models.Gemini37Flash}
	nextCfg.Providers["gemini"] = gemini
	next := mustYAML(t, nextCfg)
	stub := &appConfigAgentStub{version: "1", body: initial}
	loader := newTestAppConfigLoader(t, stub)
	if _, _, err := loader.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []string
	if err := loader.Listen(func(_, _, content string) {
		mu.Lock()
		got = append(got, content)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}

	stub.set("2", next)
	waitForAppConfigCallback(t, &got, &mu, next)
}

func TestAppConfigListenIgnoresVersionOnlyChange(t *testing.T) {
	initial := mustYAML(t, validConfig())
	stub := &appConfigAgentStub{version: "1", body: initial}
	loader := newTestAppConfigLoader(t, stub)
	if _, _, err := loader.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []string
	if err := loader.Listen(func(_, _, content string) {
		mu.Lock()
		got = append(got, content)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}

	stub.set("2", initial)
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("same body must not callback, got %#v", got)
	}
}

func TestAppConfigCloseStopsListen(t *testing.T) {
	initial := mustYAML(t, validConfig())
	nextCfg := validConfig()
	gemini := nextCfg.Providers["gemini"]
	gemini.Models = []string{models.Gemini25Flash, models.Gemini25FlashImage, models.Gemini37Flash}
	nextCfg.Providers["gemini"] = gemini
	next := mustYAML(t, nextCfg)
	stub := &appConfigAgentStub{version: "1", body: initial}
	loader := newTestAppConfigLoader(t, stub)
	if _, _, err := loader.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []string
	if err := loader.Listen(func(_, _, content string) {
		mu.Lock()
		got = append(got, content)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	loader.Close()

	stub.set("2", next)
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("Close must stop callbacks, got %#v", got)
	}
}
