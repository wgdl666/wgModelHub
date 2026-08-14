.PHONY: generate test build

generate:
	protoc \
		--go_out=. --go_opt=module=github.com/wgdl666/wgModelHub \
		--go-grpc_out=. --go-grpc_opt=module=github.com/wgdl666/wgModelHub \
		proto/wg_model_hub/v1/model_hub.proto

test:
	go test ./...

build:
	go build -o bin/wg-model-hub ./cmd/server
