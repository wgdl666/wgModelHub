.PHONY: generate test build

# protoc 插件安装在 GOPATH/bin；显式加入 PATH，避免 CI/本地环境找不到。
export PATH := $(shell go env GOPATH)/bin:$(PATH)

generate:
	protoc \
		-I . \
		-I third_party/googleapis \
		--go_out=. --go_opt=module=github.com/wgdl666/wgModelHub \
		--go-grpc_out=. --go-grpc_opt=module=github.com/wgdl666/wgModelHub \
		proto/wg_model_hub/v2/model_hub.proto

test:
	go test ./...

build:
	go build -o bin/wg-model-hub ./cmd/server
