package config

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type RuntimeLoader interface {
	Load(context.Context) (Config, string, error)
	Listen(func(dataID, group, content string)) error
	Close()
}

var (
	runtimeBootstrapFilePath = BootstrapFilePath
	newNacosRuntimeLoader    = func(bootstrap Bootstrap) (RuntimeLoader, error) {
		return NewNacosConfigLoader(bootstrap)
	}
)

// NewRuntimeLoaderFromEnv 只按 XX_WG_REGION 选择配置源。
// CN/空走 Nacos；SG/US 走 AppConfig。忽略 WG_CONFIG_SOURCE，避免旧开关把 ACK 误切海外。
func NewRuntimeLoaderFromEnv() (RuntimeLoader, error) {
	region := strings.ToUpper(strings.TrimSpace(os.Getenv("XX_WG_REGION")))
	switch region {
	case "", "CN":
		bootstrap, err := LoadBootstrapFile(runtimeBootstrapFilePath)
		if err != nil {
			return nil, err
		}
		return newNacosRuntimeLoader(bootstrap)
	case "SG", "US":
		return NewAppConfigLoaderFromEnv()
	default:
		return nil, fmt.Errorf("unsupported XX_WG_REGION %q", region)
	}
}
