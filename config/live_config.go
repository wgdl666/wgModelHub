package config

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"

	"github.com/wgdl666/kangaroo/logs"
	"gopkg.in/yaml.v3"
)

// LiveConfig 通过 loader.Listen 回调原子切换整份 Config；禁止在原 Config 上逐字段修改。
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

// Load 返回当前配置副本；models / model_routes 等热字段在请求路径读取。
func (lc *LiveConfig) Load() Config {
	if lc == nil {
		return Config{}
	}
	if p := lc.current.Load(); p != nil {
		return *p
	}
	return Config{}
}

// ApplyYAML 解析/校验后切换；restart 字段变化或非法 YAML 整单拒绝并保留旧指针。
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

	// 监听地址由 env 注入，不在 YAML 中；热更新须保留当前运行时值。
	next.Server.ListenAddress = previous.Server.ListenAddress
	next.Server.PublicListenAddress = previous.Server.PublicListenAddress
	next.Server.HTTPListenAddress = previous.Server.HTTPListenAddress
	lc.Store(next)
	logs.Default().Info("nacos_config_applied")
}

// ParseAndValidateYAML 与启动路径相同：忽略未知字段、拒绝多文档，并执行完整 Validate。
// 控制台与跨环境 YAML 常残留已下线或预留键；严格拒绝对齐会阻断启动/热更新，故忽略多余字段，已知字段仍加载并由 Validate 约束取值。
func ParseAndValidateYAML(content string) (Config, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		// 第二份文档即使能解码进空 struct，也必须拒绝；不能把 nil decode 误差当作成功。
		if err == nil {
			return Config{}, fmt.Errorf("Nacos YAML config contains multiple documents")
		}
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// RestartRequiredFields 列出绑定启动期连接/资源的字段。
// 同名 provider 的 Models、顶层 model_routes、vworld_image.base_url，以及实例名可热更新。
// 实例名只是 map 键。连接参数能和启动时的客户端一一对上时，改名不重建连接。
// 薇光生图地址按请求读取，路演可以把公网入口切到俄亥俄内网而不重启。
// 增删实例、凭据、代理和其他供应商端点仍整单拒绝，避免配置已记下、进程还在连旧地址。
func RestartRequiredFields(previous, next Config) []string {
	var fields []string
	if previous.Database.DSN != next.Database.DSN {
		fields = append(fields, "database.dsn")
	}
	if previous.Logfire != next.Logfire {
		fields = append(fields, "logfire")
	}
	if previous.ObjectStorage != next.ObjectStorage {
		fields = append(fields, "object_storage")
	}
	if !providerResourcesEqual(previous.Providers, next.Providers) {
		fields = append(fields, "providers")
	}
	return fields
}

// providerResourcesEqual 只比较启动期绑定的连接与资源参数；Models 是路由元数据，不参与重启判定。
// vworld_image.base_url 也不参与：运行中的生图客户端每次请求都会重读它。
// 键名本身不是连接。同名实例必须资源不变；只改名的键按资源一对一配对，配不上或两家资源相同无法分辨时拒绝。
func providerResourcesEqual(previous, next map[string]ProviderConfig) bool {
	if len(previous) != len(next) {
		return false
	}
	var pendingPrev, pendingNext []ProviderConfig
	for name, prev := range previous {
		curr, ok := next[name]
		if !ok {
			pendingPrev = append(pendingPrev, prev)
			continue
		}
		if !providerResourceEqual(prev, curr) {
			return false
		}
	}
	for name, curr := range next {
		if _, ok := previous[name]; !ok {
			pendingNext = append(pendingNext, curr)
		}
	}
	if len(pendingPrev) != len(pendingNext) {
		return false
	}
	used := make([]bool, len(pendingNext))
	for _, prev := range pendingPrev {
		match := -1
		count := 0
		for i, curr := range pendingNext {
			if used[i] || !providerResourceEqual(prev, curr) {
				continue
			}
			count++
			match = i
		}
		// 两家连接参数相同就分不清客户端该跟哪个新名字，不能热更新。
		if count != 1 {
			return false
		}
		used[match] = true
	}
	return true
}

// LookupStartedProvider 把当前配置里的实例名对回启动时建好的客户端键。
// 名字没变就用原键。改名后，已经从配置消失的旧键里只接受唯一一个资源相同的。
// 仍留在配置里的实例不参与匹配，避免和同资源的另一家抢客户端。
func LookupStartedProvider(started, live map[string]ProviderConfig, name string) (string, bool) {
	if _, ok := started[name]; ok {
		return name, true
	}
	current, ok := live[name]
	if !ok {
		return "", false
	}
	matched := ""
	count := 0
	for oldName, oldCfg := range started {
		if _, still := live[oldName]; still {
			continue
		}
		if !providerResourceEqual(oldCfg, current) {
			continue
		}
		count++
		matched = oldName
	}
	if count != 1 {
		return "", false
	}
	return matched, true
}

func providerResourceEqual(previous, next ProviderConfig) bool {
	left := previous
	right := next
	left.Models = nil
	right.Models = nil
	// Vertex 的 project、proxy_url 留在比较里：客户端在启动时固定，变更必须重启。
	left.VWorldImage = vworldImageWithoutBaseURL(left.VWorldImage)
	right.VWorldImage = vworldImageWithoutBaseURL(right.VWorldImage)
	return reflect.DeepEqual(left, right)
}

// vworldImageWithoutBaseURL 去掉可热更新的地址后再比较。用户名和密码仍是启动期凭据。
// 复制结构体，避免把正在服务的配置指针改掉。
func vworldImageWithoutBaseURL(cfg *VWorldImageProviderConfig) *VWorldImageProviderConfig {
	if cfg == nil {
		return nil
	}
	copied := *cfg
	copied.BaseURL = ""
	return &copied
}
