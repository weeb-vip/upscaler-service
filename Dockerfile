# Build the Go service.
FROM golang:1.26 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o main ./cmd/main.go

# Runtime: Real-ESRGAN on the CPU through ONNX Runtime. The ncnn/Vulkan
# build the authors ship is GPU-only; on a software Vulkan driver it needed
# more than 3 GB and ten minutes for one poster, memory growing with every
# tile. The ONNX graphs in runner/models are the SRVGG checkpoints
# converted once (runner/convert.py); inference is tiled, so memory is
# bounded by the tile (about 700 MB), and a poster takes seconds to tens
# of seconds depending on the CPU.
FROM python:3.12-slim
WORKDIR /app
COPY runner/requirements.txt /app/runner/requirements.txt
RUN pip install --no-cache-dir -r /app/runner/requirements.txt
COPY runner/upscale.py /app/runner/upscale.py
COPY runner/models/*.onnx /app/runner/models/
COPY --from=builder /app/main .
ARG VERSION
ENV VERSION=$VERSION \
    UPSCALER_BINARY=/app/runner/upscale.py \
    UPSCALER_MODELS_DIR=/app/runner/models \
    UPSCALER_MODEL=realesr-general-x4v3 \
    PORT=3000
EXPOSE 3000
RUN useradd -r -u 10001 upscaler && chown -R upscaler /app && chmod +x /app/runner/upscale.py
USER upscaler
CMD ["./main", "serve"]
