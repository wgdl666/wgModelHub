package config

import (
	"io"
	"reflect"
	"strings"
	"sync/atomic"

	"github.com/wgdl666/kangaroo/logs"
	"gopkg.in/yaml.v3"
)

// LiveConfig 通过 Nacos ListenConfig 回调原子切换整份 Config；禁止在原 Config 上逐字段修改。
type LiveConfig struct {
	current atomic.Pointer[Config]
}

// NewLiveConfig 以启动期已校验配置作为首份指针。
func NewLiveConfig(initial Config) *LiveConfig {
	lc := &LiveConfig{}
	lc.Store(initial)
	return lc
}

// Store 发布新配置副本；仅 ApplyYAML 成功路径或启动装配应调用。
func (lc *LiveConfig) Store(cfg Config) {
	c := cfg
	lc.current.Store(&c)
}

// Load 返回当前配置副本；model_routes 等热字段在请求路径读取。
func (lc *LiveConfig) Load() Config {
	if lc == nil {
		return Config{}
	}
	if p := lc.current.Load(); p != nil {
		return *p
	}
	return Config{}
}

// ApplyYAML 严格解析/校验后切换；restart 字段变化或非法 YAML 整单拒绝并保留旧指针。
func (lc *LiveConfig) ApplyYAML(content string) {
	next, err := ParseAndValidateYAML(content)
	if err != nil {
		logs.Default().Error("nacos_config_rejected", "reason", err.Error())
		return
	}

	previous := lc.Load()
	if fields := RestartRequiredFields(previous, next); len(fields) > 0 {
		logs.Default().Error("nacos_config_restart_required", "fields", strings.Join(fields, ","))
		return
	}

	lc.Store(next)
	logs.Default().Info("nacos_config_applied")
}

// ParseAndValidateYAML 与启动路径相同：拒绝未知字段、多文档，并执行完整 Validate。
func ParseAndValidateYAML(content string) (Config, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// RestartRequiredFields 列出绑定启动期连接/资源的字段；provider 集合或凭据变化必须滚动重启。
func RestartRequiredFields(previous, next Config) []string {
	var fields []string
	if strings.TrimSpace(previous.Server.ListenAddress) != strings.TrimSpace(next.Server.ListenAddress) {
		fields = append(fields, "server.listen_address")
	}
	if previous.Database.DSN != next.Database.DSN {
		fields = append(fields, "database.dsn")
	}
	if previous.Logfire != next.Logfire {
		fields = append(fields, "logfire")
	}
	if !reflect.DeepEqual(previous.Providers, next.Providers) {
		fields = append(fields, "providers")
	}
	return fields
}
