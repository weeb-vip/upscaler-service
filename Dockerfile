# Build the Go service.
FROM golang:1.26 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o main ./cmd/main.go

# Fetch the Real-ESRGAN ncnn build the authors ship: one static executable
# plus the model files. Pinned by release.
FROM debian:bookworm-slim AS esrgan
ARG ESRGAN_RELEASE=v0.2.5.0
ARG ESRGAN_BUILD=20220424
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates unzip \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /tmp/esrgan.zip \
      "https://github.com/xinntao/Real-ESRGAN/releases/download/${ESRGAN_RELEASE}/realesrgan-ncnn-vulkan-${ESRGAN_BUILD}-ubuntu.zip" \
    && mkdir -p /opt/esrgan && unzip -q /tmp/esrgan.zip -d /opt/esrgan \
    && chmod +x /opt/esrgan/realesrgan-ncnn-vulkan \
    && rm -f /tmp/esrgan.zip /opt/esrgan/*.mp4 /opt/esrgan/input*.jpg

# Runtime: the ncnn build needs a Vulkan loader. With no GPU in the pod,
# Mesa's lavapipe is a software Vulkan device, so it still runs -- slowly.
# A node with a GPU and its Vulkan ICD mounted makes the same image fast.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      libvulkan1 mesa-vulkan-drivers libgomp1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=esrgan /opt/esrgan/realesrgan-ncnn-vulkan /usr/local/bin/realesrgan-ncnn-vulkan
COPY --from=esrgan /opt/esrgan/models /app/models
COPY --from=builder /app/main .
ARG VERSION
ENV VERSION=$VERSION \
    UPSCALER_MODELS_DIR=/app/models \
    PORT=3000
EXPOSE 3000
RUN useradd -r -u 10001 upscaler && chown -R upscaler /app
USER upscaler
CMD ["./main", "serve"]
