// Package config reads the service's settings from the environment, with
// defaults that run the stock Real-ESRGAN ncnn build out of the box.
package config

import (
	"os"
	"strconv"
	"strings"
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
	// Model is the network to run. realesrgan-x4plus is the general one: the
	// catalogue is promotional posters that mix illustration, painted
	// backgrounds, logos and typography, and the anime model redraws all of
	// it as flat colour and clean strokes -- small text came out as invented
	// letter shapes. The general model keeps text and gradients honest at the
	// cost of slightly softer line art.
	Model string
	// Scale is the factor to run at. 2 by default, not the model's native 4:
	// the object at the key is capped at a display width anyway (1000px for
	// a poster), so 4x of a 680px poster is 2720px of work thrown away --
	// and on lavapipe that output needs more than 3 GB and gets the pod
	// OOM-killed. 2x lands at 1360px, within the cap for banners and just
	// above it for posters, for a quarter of the memory.
	Scale int
	// GPU is the Vulkan device index, or "auto" to let the binary pick. There
	// is no CPU value: a software Vulkan driver (Mesa's lavapipe) presents
	// itself as device 0, and -1 makes the binary answer "invalid gpu device".
	GPU string
	// Tile size; 0 lets the binary decide. Smaller tiles need less memory.
	Tile int
	// Format of the produced image: png, jpg or webp.
	Format string
	// Timeout for one image. CPU runs of a large image can take minutes.
	Timeout time.Duration
	// MaxUploadBytes caps a request body.
	MaxUploadBytes int64

	Bucket   BucketConfig
	Pipeline PipelineConfig
	Nats     NatsConfig
	// Cloudflare cache purge for replaced objects; both empty disables it.
	CloudflareZoneID string
	CloudflareToken  string
}

// BucketConfig uses image-sync's variable names, so the two deployments can
// share one set of values.
type BucketConfig struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	UseSSL          bool
	Bucket          string
	// Prefix is the part of the key before image-sync's leading-slashed path:
	// "weeb" in production, "weeb-staging" on staging.
	Prefix string
}

type PipelineConfig struct {
	// MinWidth: an image at least this wide is not upscaled.
	MinWidth int
	// KeepOriginal keeps a copy at <key>-orig before replacing.
	KeepOriginal bool
	OrigSuffix   string
	// Format the replacement is written in: jpg or webp.
	Format string
	// CDNBase is the public origin objects are served from, for the purge.
	CDNBase string
	// Scales per kind (anime, poster, banner, character, staff, work); a kind
	// left out uses UPSCALER_SCALE.
	Scales map[string]int
	// DisplayWidths per kind: the cap on what is stored at the key. Left out
	// means the pipeline's defaults (600 roots, 1000 posters, 1920 banners).
	DisplayWidths map[string]int
	// KeepFull stores the uncapped result at <key>-4x.
	KeepFull bool
	// DisplayQuality of the capped JPEG.
	DisplayQuality int
}

type NatsConfig struct {
	URL               string
	ConsumerGroupName string
	StreamName        string
	Offset            string
	// Subject is where image-sync announces stored objects.
	Subject string
	// Workers upscale concurrently. One per GPU; on a CPU more than two
	// just contend.
	Workers int
}

func Load() Config {
	return Config{
		Port:           intEnv("PORT", 3000),
		Binary:         env("UPSCALER_BINARY", "realesrgan-ncnn-vulkan"),
		ModelsDir:      env("UPSCALER_MODELS_DIR", ""),
		Model:          env("UPSCALER_MODEL", "realesrgan-x4plus"),
		Scale:          intEnv("UPSCALER_SCALE", 2),
		GPU:            env("UPSCALER_GPU", "auto"),
		Tile:           intEnv("UPSCALER_TILE", 0),
		Format:         env("UPSCALER_FORMAT", "png"),
		Timeout:        durationEnv("UPSCALER_TIMEOUT", 10*time.Minute),
		MaxUploadBytes: int64(intEnv("UPSCALER_MAX_UPLOAD_MB", 20)) << 20,
		Bucket: BucketConfig{
			Endpoint:        env("MINIO_ENDPOINT", "localhost:9000"),
			AccessKeyID:     env("MINIO_ACCESS_KEY_ID", "minio"),
			SecretAccessKey: env("MINIO_SECRET_ACCESS_KEY", "minio123"),
			UseSSL:          env("MINIO_USESSL", "false") == "true",
			Bucket:          env("MINIO_BUCKET", "weeb"),
			Prefix:          env("MINIO_PREFIX", "weeb"),
		},
		Pipeline: PipelineConfig{
			MinWidth:       intEnv("UPSCALER_MIN_WIDTH", 1000),
			KeepOriginal:   env("UPSCALER_KEEP_ORIGINAL", "true") == "true",
			OrigSuffix:     env("UPSCALER_ORIG_SUFFIX", "-orig"),
			Format:         env("UPSCALER_BUCKET_FORMAT", "jpg"),
			CDNBase:        env("CDN_BASE_URL", "https://cdn.weeb.vip"),
			Scales:         scalesByKind(),
			DisplayWidths:  byKind("UPSCALER_MAX_WIDTH_"),
			KeepFull:       env("UPSCALER_KEEP_FULL", "true") == "true",
			DisplayQuality: intEnv("UPSCALER_DISPLAY_QUALITY", 90),
		},
		Nats: NatsConfig{
			URL:               env("NATSURL", "nats://localhost:4222"),
			ConsumerGroupName: env("NATSCONSUMERGROUPNAME", "upscaler-service"),
			StreamName:        env("NATSSTREAMNAME", ""),
			Offset:            env("NATSOFFSET", "earliest"),
			Subject:           env("NATSSUBJECT", "image-stored"),
			Workers:           intEnv("UPSCALER_WORKERS", 1),
		},
		CloudflareZoneID: env("CLOUDFLARE_ZONE_ID", ""),
		CloudflareToken:  env("CLOUDFLARE_API_TOKEN", ""),
	}
}

// UPSCALER_SCALE_ANIME=2 and friends: one variable per kind, read only when
// set, so the runner's default covers the rest.
func scalesByKind() map[string]int { return byKind("UPSCALER_SCALE_") }

func byKind(prefix string) map[string]int {
	out := map[string]int{}
	for _, kind := range []string{"anime", "poster", "banner", "character", "staff", "work"} {
		if n := intEnv(prefix+strings.ToUpper(kind), 0); n > 0 {
			out[kind] = n
		}
	}
	return out
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
