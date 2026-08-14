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
	"github.com/wgdl666/wgModelHub/internal/profile"
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

// ProviderConfig 是受控的供应商连接配置。字段集合由代码固定，
// 调用方不能借 profile 注入地址、密钥或任意供应商参数。
type ProviderConfig struct {
	Type               string  `yaml:"type"`
	APIKey             string  `yaml:"api_key"`
	BaseURL            string  `yaml:"base_url"`
	ProxyURL           string  `yaml:"proxy_url"`
	Project            string  `yaml:"project"`
	Location           string  `yaml:"location"`
	Token              string  `yaml:"token"`
	SendEnableThinking bool    `yaml:"send_enable_thinking"`
	Duration           float64 `yaml:"duration"`
	FPS                int     `yaml:"fps"`
	Seed               int     `yaml:"seed"`
	PollInterval       float64 `yaml:"poll_interval"`
	MaxPollTime        float64 `yaml:"max_poll_time"`
}

// ProfileConfig 把一个逻辑业务 profile 固定到一个供应商模型。
// 第一版不包含候选列表、权重或自动降级语义。
type ProfileConfig struct {
	Capability string `yaml:"capability"`
	Provider   string `yaml:"provider"`
	Model      string `yaml:"model"`
}

type Config struct {
	Server struct {
		ListenAddress string `yaml:"listen_address"`
	} `yaml:"server"`
	Providers map[string]ProviderConfig `yaml:"providers"`
	Profiles  map[string]ProfileConfig  `yaml:"profiles"`
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
	if len(c.Providers) == 0 {
		return fmt.Errorf("providers are required")
	}
	if len(c.Profiles) == 0 {
		return fmt.Errorf("profiles are required")
	}

	for name, provider := range c.Providers {
		if err := validateProvider(name, provider); err != nil {
			return err
		}
	}
	for name, profileCfg := range c.Profiles {
		provider, ok := c.Providers[profileCfg.Provider]
		if !ok {
			return fmt.Errorf("profile %s references unknown provider %s", name, profileCfg.Provider)
		}
		if strings.TrimSpace(profileCfg.Model) == "" {
			return fmt.Errorf("profile %s model is required", name)
		}
		if !providerSupports(provider.Type, profileCfg.Capability) {
			return fmt.Errorf("profile %s capability %s is not supported by provider %s", name, profileCfg.Capability, profileCfg.Provider)
		}
	}
	// 稳定 profile 缺一不可：调用方常量与 Nacos 映射必须在启动前对齐。
	for _, name := range profile.Required() {
		if _, ok := c.Profiles[name]; !ok {
			return fmt.Errorf("required profile %s is missing", name)
		}
	}
	return nil
}

func validateProvider(name string, provider ProviderConfig) error {
	providerType := strings.ToLower(strings.TrimSpace(provider.Type))
	switch providerType {
	case "gemini":
		if strings.TrimSpace(provider.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case "vertexai":
		if strings.TrimSpace(provider.Project) == "" || strings.TrimSpace(provider.Location) == "" {
			return fmt.Errorf("provider %s project and location are required", name)
		}
	case "ark", "openai":
		if strings.TrimSpace(provider.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case "ltx":
		if strings.TrimSpace(provider.BaseURL) == "" ||
			provider.Duration <= 0 ||
			provider.FPS <= 0 ||
			provider.PollInterval <= 0 ||
			provider.MaxPollTime <= 0 {
			return fmt.Errorf("provider %s LTX configuration is incomplete", name)
		}
	default:
		return fmt.Errorf("provider %s type %q is unsupported", name, provider.Type)
	}
	return nil
}

func providerSupports(providerType, capability string) bool {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "gemini":
		return capability == CapabilityText || capability == CapabilityImage
	case "vertexai", "ark", "openai":
		return capability == CapabilityText
	case "ltx":
		return capability == CapabilityVideo
	default:
		return false
	}
}
