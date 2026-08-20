package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"gopkg.in/yaml.v3"
)

const (
	BootstrapFilePath = "/etc/wg-model-hub/bootstrap.json"
	NacosDataID       = "wg.mirror.modelHub"
	NacosGroup        = "DEFAULT_GROUP"

	CapabilityText  = "text"
	CapabilityImage = "image"
	CapabilityVideo = "video"
)

// Bootstrap 只保存 Nacos 定位信息；供应商凭据只能存在于受保护的配置正文中。
type Bootstrap struct {
	ServerAddress string `json:"server_address"`
	NamespaceID   string `json:"namespace_id"`
}

// ProviderConfig 用互斥嵌套字段表达具体供应商；每个实例必须且只能配置一种，
// 并显式声明该实例承载的真实模型 ID 列表（含版本）。
type ProviderConfig struct {
	Models   []string                `yaml:"models"`
	Gemini   *GeminiProviderConfig   `yaml:"gemini"`
	VertexAI *VertexAIProviderConfig `yaml:"vertexai"`
	Ark      *ArkProviderConfig      `yaml:"ark"`
	OpenAI   *OpenAIProviderConfig   `yaml:"openai"`
	LTX      *LTXProviderConfig      `yaml:"ltx"`
}

type GeminiProviderConfig struct {
	APIKey   string `yaml:"api_key"`
	BaseURL  string `yaml:"base_url"`
	ProxyURL string `yaml:"proxy_url"`
}

type VertexAIProviderConfig struct {
	Project  string `yaml:"project"`
	Location string `yaml:"location"`
}

type ArkProviderConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
}

// OpenAIProviderConfig 覆盖 OpenAI-compatible HTTP 端；OminiLink 文本也走这里。
type OpenAIProviderConfig struct {
	APIKey             string `yaml:"api_key"`
	BaseURL            string `yaml:"base_url"`
	SendEnableThinking bool   `yaml:"send_enable_thinking"`
}

type LTXProviderConfig struct {
	BaseURL      string  `yaml:"base_url"`
	Token        string  `yaml:"token"`
	Duration     float64 `yaml:"duration"`
	FPS          int     `yaml:"fps"`
	Seed         int     `yaml:"seed"`
	PollInterval float64 `yaml:"poll_interval"`
	MaxPollTime  float64 `yaml:"max_poll_time"`
}

type Config struct {
	Server struct {
		ListenAddress string `yaml:"listen_address"`
	} `yaml:"server"`
	Providers map[string]ProviderConfig `yaml:"providers"`
	// Logfire 与 Hub 等同项目；token 写在本服务 Nacos，禁止再挂 wg-hub-env。
	Logfire LogfireConfig `yaml:"logfire"`
}

// LogfireConfig 是跨服务 Trace 导出到同一 Logfire 项目的凭据与身份。
type LogfireConfig struct {
	Token        string `yaml:"token"`
	Env          string `yaml:"env"`
	Service      string `yaml:"service"`
	Version      string `yaml:"version"`
	OtelLogLevel string `yaml:"otel_log_level"`
}

func LoadBootstrapFile(path string) (Bootstrap, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Bootstrap{}, fmt.Errorf("read Nacos bootstrap: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var bootstrap Bootstrap
	if err := decoder.Decode(&bootstrap); err != nil {
		return Bootstrap{}, fmt.Errorf("decode Nacos bootstrap: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Bootstrap{}, fmt.Errorf("Nacos bootstrap contains trailing content")
	}
	bootstrap.ServerAddress = strings.TrimSpace(bootstrap.ServerAddress)
	bootstrap.NamespaceID = strings.TrimSpace(bootstrap.NamespaceID)
	if err := bootstrap.Validate(); err != nil {
		return Bootstrap{}, err
	}
	return bootstrap, nil
}

func (b Bootstrap) Validate() error {
	if b.ServerAddress == "" {
		return fmt.Errorf("Nacos server_address is required")
	}
	_, portText, err := net.SplitHostPort(b.ServerAddress)
	if err != nil {
		return fmt.Errorf("Nacos server_address must be host:port: %w", err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("Nacos server_address port is invalid")
	}
	return nil
}

func newNacosClientConfig(namespaceID string) *constant.ClientConfig {
	options := []constant.ClientOption{
		constant.WithTimeoutMs(10_000),
		constant.WithNotLoadCacheAtStart(true),
		constant.WithAppName("wg-model-hub"),
		constant.WithLogLevel("warn"),
		// Pod 使用非 root 用户，Nacos SDK 的日志和缓存只能写入临时目录。
		constant.WithLogDir("/tmp/wg-model-hub/nacos/log"),
		constant.WithCacheDir("/tmp/wg-model-hub/nacos/cache"),
		constant.WithRamConfig(&constant.RamConfig{}),
	}
	if namespaceID = strings.TrimSpace(namespaceID); namespaceID != "" {
		options = append(options, constant.WithNamespaceId(namespaceID))
	}
	return constant.NewClientConfig(options...)
}

type configGetter interface {
	GetConfig(vo.ConfigParam) (string, error)
}

// NacosConfigLoader 只读取固定 Data ID，并在建立供应商客户端前完成严格校验。
type NacosConfigLoader struct {
	client configGetter
	close  func()
}

func NewNacosConfigLoader(bootstrap Bootstrap) (*NacosConfigLoader, error) {
	if err := bootstrap.Validate(); err != nil {
		return nil, err
	}
	host, portText, _ := net.SplitHostPort(bootstrap.ServerAddress)
	port, _ := strconv.ParseUint(portText, 10, 16)
	clientConfig := *newNacosClientConfig(bootstrap.NamespaceID)
	client, err := clients.NewConfigClient(vo.NacosClientParam{
		ClientConfig:  &clientConfig,
		ServerConfigs: []constant.ServerConfig{*constant.NewServerConfig(host, port)},
	})
	if err != nil {
		return nil, fmt.Errorf("create Nacos client: %w", err)
	}
	return &NacosConfigLoader{client: client, close: client.CloseClient}, nil
}

func (l *NacosConfigLoader) Load(ctx context.Context) (Config, error) {
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	content, err := l.client.GetConfig(vo.ConfigParam{DataId: NacosDataID, Group: NacosGroup})
	if err != nil {
		return Config{}, fmt.Errorf("read Nacos config: %w", err)
	}
	decoder := yaml.NewDecoder(strings.NewReader(content))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode Nacos YAML: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Config{}, fmt.Errorf("Nacos YAML contains multiple documents")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (l *NacosConfigLoader) Close() {
	if l != nil && l.close != nil {
		l.close()
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Server.ListenAddress) == "" {
		return fmt.Errorf("server.listen_address is required")
	}
	if strings.TrimSpace(c.Logfire.Token) == "" {
		return fmt.Errorf("logfire.token is required")
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("providers are required")
	}

	// 真实模型 ID 全局唯一：启动时建 model -> provider 路由，重复或空模型直接失败。
	seenModels := make(map[string]string)
	for name, provider := range c.Providers {
		if err := validateProvider(name, provider); err != nil {
			return err
		}
		if len(provider.Models) == 0 {
			return fmt.Errorf("provider %s models are required", name)
		}
		for _, model := range provider.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				return fmt.Errorf("provider %s contains an empty model id", name)
			}
			if other, ok := seenModels[model]; ok {
				return fmt.Errorf("model %s is bound to both provider %s and %s", model, other, name)
			}
			seenModels[model] = name
		}
	}
	return nil
}

// ModelRoutes 返回真实模型 ID -> provider 实例名；Validate 已保证唯一。
func (c Config) ModelRoutes() map[string]string {
	routes := make(map[string]string)
	for name, provider := range c.Providers {
		for _, model := range provider.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			routes[model] = name
		}
	}
	return routes
}

func validateProvider(name string, provider ProviderConfig) error {
	switch countConcreteProviders(provider) {
	case 0:
		return fmt.Errorf("provider %s must set exactly one of gemini/vertexai/ark/openai/ltx", name)
	case 1:
	default:
		return fmt.Errorf("provider %s must set exactly one of gemini/vertexai/ark/openai/ltx", name)
	}
	switch {
	case provider.Gemini != nil:
		if strings.TrimSpace(provider.Gemini.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.VertexAI != nil:
		if strings.TrimSpace(provider.VertexAI.Project) == "" || strings.TrimSpace(provider.VertexAI.Location) == "" {
			return fmt.Errorf("provider %s project and location are required", name)
		}
	case provider.Ark != nil:
		if strings.TrimSpace(provider.Ark.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.OpenAI != nil:
		if strings.TrimSpace(provider.OpenAI.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.LTX != nil:
		ltx := provider.LTX
		if strings.TrimSpace(ltx.BaseURL) == "" ||
			ltx.Duration <= 0 ||
			ltx.FPS <= 0 ||
			ltx.PollInterval <= 0 ||
			ltx.MaxPollTime <= 0 {
			return fmt.Errorf("provider %s LTX configuration is incomplete", name)
		}
	}
	return nil
}

func countConcreteProviders(provider ProviderConfig) int {
	n := 0
	if provider.Gemini != nil {
		n++
	}
	if provider.VertexAI != nil {
		n++
	}
	if provider.Ark != nil {
		n++
	}
	if provider.OpenAI != nil {
		n++
	}
	if provider.LTX != nil {
		n++
	}
	return n
}

// ProviderSupports 根据供应商类型判断能否承接 OutputSpec 对应能力。
func ProviderSupports(provider ProviderConfig, capability string) bool {
	switch {
	case provider.Gemini != nil:
		return capability == CapabilityText || capability == CapabilityImage
	case provider.VertexAI != nil, provider.Ark != nil, provider.OpenAI != nil:
		return capability == CapabilityText
	case provider.LTX != nil:
		return capability == CapabilityVideo
	default:
		return false
	}
}
