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

// resolveAppConfigCoord 只拼 service_name/env，路演不再用 tomirro-modelhub。
func resolveAppConfigCoord(serviceName, region, envName string) (appConfigCoord, error) {
	serviceName = strings.TrimSpace(serviceName)
	region = strings.ToUpper(strings.TrimSpace(region))
	envName = strings.TrimSpace(envName)
	if serviceName != requiredServiceName {
		return appConfigCoord{}, fmt.Errorf("XX_WG_SERVICE_NAME must be %q", requiredServiceName)
	}
	if region != "SG" && region != "US" {
		return appConfigCoord{}, fmt.Errorf("AppConfig identity requires XX_WG_REGION SG or US")
	}
	if !validAppConfigEnv(envName) {
		return appConfigCoord{}, fmt.Errorf("unsupported AppConfig identity %s/%s/%s", serviceName, region, envName)
	}
	return appConfigCoord{
		Application: serviceName,
		Environment: envName,
		Profile:     "config-" + envName,
	}, nil
}

func validAppConfigEnv(envName string) bool {
	return envName == "prod" || envName == "dev" || strings.HasPrefix(envName, "ppe_")
}

func readPlatformIdentity() (serviceName, region, envName string) {
	return strings.TrimSpace(os.Getenv("XX_WG_SERVICE_NAME")),
		strings.ToUpper(strings.TrimSpace(os.Getenv("XX_WG_REGION"))),
		strings.TrimSpace(os.Getenv("XX_WG_ENV"))
}
