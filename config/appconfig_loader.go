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
	"sync"
	"time"

	"github.com/wgdl666/kangaroo/logs"
)

const (
	defaultAppConfigAgentEndpoint = "http://127.0.0.1:2772"
	maxAppConfigBytes             = 256 << 10
	defaultAppConfigPollInterval  = 45 * time.Second
	appConfigVersionHeader        = "Configuration-Version"
)

type AppConfigLoader struct {
	client       *http.Client
	url          string
	pollInterval time.Duration

	mu          sync.Mutex
	lastVersion string
	lastBody    string
	stop        context.CancelFunc
	done        chan struct{}
}

func NewAppConfigLoaderFromEnv() (*AppConfigLoader, error) {
	coord, err := resolveAppConfigCoord(readPlatformIdentity())
	if err != nil {
		return nil, err
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
		coord.Application,
		coord.Environment,
		coord.Profile,
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
	version, raw, err := l.fetch(ctx)
	if err != nil {
		return Config{}, "", err
	}
	cfg, err := ParseAndValidateYAML(raw)
	if err != nil {
		return Config{}, "", fmt.Errorf("AppConfig configuration is invalid")
	}
	// 启动快照记下版本和正文，Listen 首轮 GET 才不会把同一份 YAML 再送进 ApplyYAML。
	l.remember(version, raw)
	return cfg, raw, nil
}

// Listen 轮询本机 Agent。AWS 没有 Nacos ListenConfig 那种推送，官方用法是应用自己 GET sidecar 缓存。
func (l *AppConfigLoader) Listen(onChange func(dataID, group, content string)) error {
	if l == nil || l.client == nil || l.url == "" {
		return fmt.Errorf("AppConfig loader is not initialized")
	}
	interval := l.pollInterval
	if interval <= 0 {
		interval = defaultAppConfigPollInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.mu.Lock()
	if l.stop != nil {
		l.mu.Unlock()
		cancel()
		return fmt.Errorf("AppConfig listen is already running")
	}
	done := make(chan struct{})
	l.stop = cancel
	l.done = done
	l.mu.Unlock()
	go l.poll(ctx, interval, onChange, done)
	return nil
}

func (l *AppConfigLoader) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	stop := l.stop
	done := l.done
	l.stop = nil
	l.done = nil
	l.mu.Unlock()
	if stop != nil {
		stop()
	}
	if done != nil {
		<-done
	}
}

func (l *AppConfigLoader) fetch(ctx context.Context) (string, string, error) {
	if l == nil || l.client == nil {
		return "", "", fmt.Errorf("AppConfig loader is not initialized")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return "", "", fmt.Errorf("create AppConfig Agent request: %w", err)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("read AppConfig Agent response: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("AppConfig Agent returned HTTP %d", resp.StatusCode)
	}

	content, err := io.ReadAll(io.LimitReader(resp.Body, maxAppConfigBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("read AppConfig Agent body: %w", err)
	}
	if len(content) == 0 {
		return "", "", fmt.Errorf("AppConfig Agent returned an empty configuration")
	}
	if len(content) > maxAppConfigBytes {
		return "", "", fmt.Errorf("AppConfig Agent configuration exceeds %d bytes", maxAppConfigBytes)
	}
	return resp.Header.Get(appConfigVersionHeader), string(content), nil
}

func (l *AppConfigLoader) poll(ctx context.Context, interval time.Duration, onChange func(dataID, group, content string), done chan struct{}) {
	defer close(done)
	l.pollOnce(ctx, onChange)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.pollOnce(ctx, onChange)
		}
	}
}

func (l *AppConfigLoader) pollOnce(ctx context.Context, onChange func(dataID, group, content string)) {
	version, body, err := l.fetch(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		logs.Default().Error("appconfig_agent_poll_failed", "reason", err.Error())
		return
	}
	l.mu.Lock()
	lastBody := l.lastBody
	l.mu.Unlock()
	if lastBody == "" || body == lastBody {
		// 无启动快照时先记住当前文，避免把启动文档当成一次热更新；同文换版本也不重复 Apply。
		l.remember(version, body)
		return
	}
	if onChange != nil {
		onChange("", "", body)
	}
	l.remember(version, body)
}

func (l *AppConfigLoader) remember(version, body string) {
	l.mu.Lock()
	l.lastVersion = version
	l.lastBody = body
	l.mu.Unlock()
}
