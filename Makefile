.PHONY: build test run docker

build:
	go build -o main ./cmd/main.go

test:
	go test ./...

# Local run against a Real-ESRGAN build on disk: `make run ESRGAN=/path/to/realesrgan-ncnn-vulkan-dir`
run: build
	UPSCALER_BINARY=$(ESRGAN)/realesrgan-ncnn-vulkan UPSCALER_MODELS_DIR=$(ESRGAN)/models ./main serve

docker:
	docker build -t upscaler-service:local .
