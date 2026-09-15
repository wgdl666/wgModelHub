package config

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultAppConfigAgentEndpoint = "http://127.0.0.1:2772"
	appConfigApplication          = "modelhub"
	appConfigRegion               = "us-east-2"
	maxAppConfigBytes             = 256 << 10
)

func allowedAppConfigApplication(got string) bool {
	return got == appConfigApplication || got == "tomirro-"+appConfigApplication
}

func allowedAppConfigEnvironment(got string) bool {
	switch got {
	case "dev", "test", "prod", "prd":
		return true
	default:
		return false
	}
}

func allowedAppConfigProfile(got string) bool {
	switch got {
	case "config-dev", "config-test", "config-prod", "config-prd":
		return true
	default:
		return false
	}
}

type AppConfigLoader struct {
	client *http.Client
	url    string
}

func NewAppConfigLoaderFromEnv() (*AppConfigLoader, error) {
	application := strings.TrimSpace(os.Getenv("APP_NAME"))
	environment := strings.TrimSpace(os.Getenv("ENV"))
	profile := strings.TrimSpace(os.Getenv("SERVICE_NAME"))
	region := strings.TrimSpace(os.Getenv("REGION"))
	switch {
	case !allowedAppConfigApplication(application):
		return nil, fmt.Errorf("APP_NAME must be %q or %q", appConfigApplication, "tomirro-"+appConfigApplication)
	case !allowedAppConfigEnvironment(environment):
		return nil, fmt.Errorf("ENV must be a supported AppConfig environment")
	case !allowedAppConfigProfile(profile):
		return nil, fmt.Errorf("SERVICE_NAME must be a supported AppConfig profile")
	case region != appConfigRegion:
		return nil, fmt.Errorf("REGION must be %q", appConfigRegion)
	}

	endpoint := strings.TrimSpace(os.Getenv("AWS_APPCONFIG_AGENT_ENDPOINT"))
	if endpoint == "" {
		endpoint = defaultAppConfigAgentEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse AWS_APPCONFIG_AGENT_ENDPOINT: %w", err)
	}
	if parsed.Scheme != "http" {
		return nil, fmt.Errorf("AWS_APPCONFIG_AGENT_ENDPOINT must use http")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("AWS_APPCONFIG_AGENT_ENDPOINT must not contain userinfo, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("AWS_APPCONFIG_AGENT_ENDPOINT must not contain a path")
	}
	if parsed.Port() == "" {
		return nil, fmt.Errorf("AWS_APPCONFIG_AGENT_ENDPOINT must include a port")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("AWS_APPCONFIG_AGENT_ENDPOINT must target loopback")
		}
	}

	parsed.Path = fmt.Sprintf(
		"/applications/%s/environments/%s/configurations/%s",
		application,
		environment,
		profile,
	)
	return &AppConfigLoader{
		client: &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		url: parsed.String(),
	}, nil
}

func (l *AppConfigLoader) Load(ctx context.Context) (Config, string, error) {
	if l == nil || l.client == nil {
		return Config{}, "", fmt.Errorf("AppConfig loader is not initialized")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return Config{}, "", fmt.Errorf("create AppConfig Agent request: %w", err)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return Config{}, "", fmt.Errorf("read AppConfig Agent response: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Config{}, "", fmt.Errorf("AppConfig Agent returned HTTP %d", resp.StatusCode)
	}

	content, err := io.ReadAll(io.LimitReader(resp.Body, maxAppConfigBytes+1))
	if err != nil {
		return Config{}, "", fmt.Errorf("read AppConfig Agent body: %w", err)
	}
	if len(content) == 0 {
		return Config{}, "", fmt.Errorf("AppConfig Agent returned an empty configuration")
	}
	if len(content) > maxAppConfigBytes {
		return Config{}, "", fmt.Errorf("AppConfig Agent configuration exceeds %d bytes", maxAppConfigBytes)
	}
	raw := string(content)
	cfg, err := ParseAndValidateYAML(raw)
	if err != nil {
		return Config{}, "", fmt.Errorf("AppConfig configuration is invalid")
	}
	return cfg, raw, nil
}

func (l *AppConfigLoader) Listen(func(dataID, group, content string)) error {
	return nil
}

func (l *AppConfigLoader) Close() {}
