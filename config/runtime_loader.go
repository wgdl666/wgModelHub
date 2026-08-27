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

func NewRuntimeLoaderFromEnv() (RuntimeLoader, error) {
	source := strings.ToLower(strings.TrimSpace(os.Getenv("WG_CONFIG_SOURCE")))
	switch source {
	case "", "nacos":
		bootstrap, err := LoadBootstrapFile(runtimeBootstrapFilePath)
		if err != nil {
			return nil, err
		}
		return newNacosRuntimeLoader(bootstrap)
	case "appconfig":
		return NewAppConfigLoaderFromEnv()
	default:
		return nil, fmt.Errorf("unsupported WG_CONFIG_SOURCE %q", source)
	}
}
