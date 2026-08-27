package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stubRuntimeLoader struct{}

func (*stubRuntimeLoader) Load(context.Context) (Config, string, error) {
	return Config{}, "", nil
}

func (*stubRuntimeLoader) Listen(func(dataID, group, content string)) error { return nil }
func (*stubRuntimeLoader) Close()                                           {}

func TestRuntimeLoaderDefaultsToNacos(t *testing.T) {
	for _, source := range []string{"", "nacos", " NACOS "} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("WG_CONFIG_SOURCE", source)
			bootstrapPath := filepath.Join(t.TempDir(), "bootstrap.json")
			if err := os.WriteFile(bootstrapPath, []byte(`{"server_address":"127.0.0.1:8848","namespace_id":"dev"}`), 0o600); err != nil {
				t.Fatal(err)
			}

			oldPath := runtimeBootstrapFilePath
			oldConstructor := newNacosRuntimeLoader
			t.Cleanup(func() {
				runtimeBootstrapFilePath = oldPath
				newNacosRuntimeLoader = oldConstructor
			})
			runtimeBootstrapFilePath = bootstrapPath
			called := false
			newNacosRuntimeLoader = func(bootstrap Bootstrap) (RuntimeLoader, error) {
				called = true
				if bootstrap.ServerAddress != "127.0.0.1:8848" || bootstrap.NamespaceID != "dev" {
					t.Fatalf("unexpected bootstrap: %#v", bootstrap)
				}
				return &stubRuntimeLoader{}, nil
			}

			loader, err := NewRuntimeLoaderFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("Nacos constructor was not called")
			}
			if _, ok := loader.(*stubRuntimeLoader); !ok {
				t.Fatalf("loader=%T", loader)
			}
		})
	}
}

func TestRuntimeLoaderSelectsAppConfig(t *testing.T) {
	t.Setenv("WG_CONFIG_SOURCE", " APPCONFIG ")
	setValidAppConfigEnv(t, "http://127.0.0.1:2772")
	loader, err := NewRuntimeLoaderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loader.(*AppConfigLoader); !ok {
		t.Fatalf("loader=%T, want *AppConfigLoader", loader)
	}
}

func TestRuntimeLoaderRejectsUnsupportedSource(t *testing.T) {
	for _, source := range []string{"file", "consul"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("WG_CONFIG_SOURCE", source)
			_, err := NewRuntimeLoaderFromEnv()
			if err == nil || !strings.Contains(err.Error(), "unsupported WG_CONFIG_SOURCE") {
				t.Fatalf("expected unsupported source error, got %v", err)
			}
		})
	}
}

func TestAppConfigListenAndCloseAreNoOps(t *testing.T) {
	loader := &AppConfigLoader{}
	called := false
	if err := loader.Listen(func(_, _, _ string) { called = true }); err != nil {
		t.Fatal(err)
	}
	loader.Close()
	if called {
		t.Fatal("AppConfig startup loader must not register a listener")
	}
}
