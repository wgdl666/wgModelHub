package config

import (
	"fmt"
	"os"
	"strings"
)

const requiredServiceName = "modelhub"

type appConfigCoord struct {
	Application string
	Environment string
	Profile     string
}

// resolveAppConfigCoord 按三项 XX_WG_* 写死文档，路演一把切到 tomirro-modelhub。
func resolveAppConfigCoord(serviceName, region, envName string) (appConfigCoord, error) {
	serviceName = strings.TrimSpace(serviceName)
	region = strings.ToUpper(strings.TrimSpace(region))
	envName = strings.TrimSpace(envName)
	if serviceName != requiredServiceName {
		return appConfigCoord{}, fmt.Errorf("XX_WG_SERVICE_NAME must be %q", requiredServiceName)
	}
	switch {
	case region == "SG" && envName == "dev":
		return appConfigCoord{Application: "modelhub", Environment: "dev", Profile: "config-dev"}, nil
	case region == "US" && envName == "ppe_exhibition":
		return appConfigCoord{Application: "tomirro-modelhub", Environment: "prod", Profile: "config-prod"}, nil
	default:
		return appConfigCoord{}, fmt.Errorf("unsupported AppConfig identity %s/%s/%s", serviceName, region, envName)
	}
}

func readPlatformIdentity() (serviceName, region, envName string) {
	return strings.TrimSpace(os.Getenv("XX_WG_SERVICE_NAME")),
		strings.ToUpper(strings.TrimSpace(os.Getenv("XX_WG_REGION"))),
		strings.TrimSpace(os.Getenv("XX_WG_ENV"))
}
