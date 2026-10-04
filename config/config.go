// Package config reads the service's settings from the environment, with
// defaults that run the stock Real-ESRGAN ncnn build out of the box.
package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	// Port the HTTP server listens on.
	Port int
	// Binary is the realesrgan-ncnn-vulkan executable, by name on PATH or by path.
	Binary string
	// ModelsDir holds the .param/.bin pairs; empty means the binary's own default
	// (a `models` directory beside it).
	ModelsDir string
	// Model is the network to run. realesrgan-x4plus-anime is trained on anime
	// illustrations and on removing JPEG artefacts from them, which is exactly
	// what the 225px MyAnimeList images are.
	Model string
	// Scale is the upscale factor the model was trained for.
	Scale int
	// GPU is the device index; -1 runs on the CPU, which is slow but needs no
	// hardware. "auto" lets the binary pick.
	GPU string
	// Tile size; 0 lets the binary decide. Smaller tiles need less memory.
	Tile int
	// Format of the produced image: png, jpg or webp.
	Format string
	// Timeout for one image. CPU runs of a large image can take minutes.
	Timeout time.Duration
	// MaxUploadBytes caps a request body.
	MaxUploadBytes int64
}

func Load() Config {
	return Config{
		Port:           intEnv("PORT", 3000),
		Binary:         env("UPSCALER_BINARY", "realesrgan-ncnn-vulkan"),
		ModelsDir:      env("UPSCALER_MODELS_DIR", ""),
		Model:          env("UPSCALER_MODEL", "realesrgan-x4plus-anime"),
		Scale:          intEnv("UPSCALER_SCALE", 4),
		GPU:            env("UPSCALER_GPU", "auto"),
		Tile:           intEnv("UPSCALER_TILE", 0),
		Format:         env("UPSCALER_FORMAT", "png"),
		Timeout:        durationEnv("UPSCALER_TIMEOUT", 10*time.Minute),
		MaxUploadBytes: int64(intEnv("UPSCALER_MAX_UPLOAD_MB", 20)) << 20,
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func intEnv(name string, fallback int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
