package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func setValidAppConfigEnv(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("APP_NAME", "modelhub")
	t.Setenv("ENV", "dev")
	t.Setenv("SERVICE_NAME", "config-dev")
	t.Setenv("REGION", "us-east-2")
	t.Setenv("AWS_APPCONFIG_AGENT_ENDPOINT", endpoint)
}

func TestNewAppConfigLoaderRejectsIdentityMismatch(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "application", key: "APP_NAME", value: "other"},
		{name: "environment", key: "ENV", value: "production"},
		{name: "profile", key: "SERVICE_NAME", value: "other"},
		{name: "region", key: "REGION", value: "ap-southeast-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setValidAppConfigEnv(t, "http://127.0.0.1:2772")
			t.Setenv(tt.key, tt.value)
			if _, err := NewAppConfigLoaderFromEnv(); err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected %s mismatch error, got %v", tt.key, err)
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
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Redirect(w, nil, "http://127.0.0.1:1/secret", http.StatusFound)
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
				_, _ = w.Write([]byte("providers: [\n"))
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
			} else if strings.Contains(err.Error(), "sensitive response body") {
				t.Fatalf("response body leaked in error: %v", err)
			}
		})
	}
}
