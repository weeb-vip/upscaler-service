// Package server is the HTTP face of the upscaler: one endpoint that takes an
// image and answers with the upscaled one.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/weeb-vip/upscaler-service/internal/upscaler"
)

type Server struct {
	up             *upscaler.Upscaler
	defaultFormat  string
	maxUploadBytes int64
}

func New(up *upscaler.Upscaler, defaultFormat string, maxUploadBytes int64) *Server {
	return &Server{up: up, defaultFormat: defaultFormat, maxUploadBytes: maxUploadBytes}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /upscale", s.upscale)
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	opts := s.up.Options()
	status := http.StatusOK
	body := map[string]any{"status": "ok", "model": opts.Model, "scale": opts.Scale, "gpu": opts.GPU}
	if err := s.up.Check(); err != nil {
		status = http.StatusServiceUnavailable
		body["status"] = "unavailable"
		body["error"] = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

/*
POST /upscale

The image is the request: either a multipart form with an `image` field, or
the raw bytes with an image Content-Type. `?format=png|jpg|webp` picks the
output and `?scale=2|3|4` the factor; the defaults are the service's. The response is the image itself, so
`curl --data-binary @in.jpg -o out.png` is the whole client.
*/
func (s *Server) upscale(w http.ResponseWriter, r *http.Request) {
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = s.defaultFormat
	}
	if format == "jpeg" {
		format = "jpg"
	}
	if !upscaler.Formats[format] {
		http.Error(w, fmt.Sprintf("unsupported format %q (png, jpg or webp)", format), http.StatusBadRequest)
		return
	}

	data, err := s.readImage(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	start := time.Now()
	scale, _ := strconv.Atoi(r.URL.Query().Get("scale"))
	if scale != 0 && (scale < 2 || scale > 4) {
		http.Error(w, "scale must be 2, 3 or 4", http.StatusBadRequest)
		return
	}
	out, err := s.up.Bytes(r.Context(), data, format, scale)
	if err != nil {
		if r.Context().Err() == context.Canceled {
			return
		}
		log.Printf("upscale failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("upscaled %d bytes -> %d bytes as %s in %s", len(data), len(out), format, time.Since(start).Round(time.Millisecond))

	w.Header().Set("Content-Type", "image/"+map[string]string{"png": "png", "jpg": "jpeg", "webp": "webp"}[format])
	w.Header().Set("X-Upscale-Duration", time.Since(start).Round(time.Millisecond).String())
	_, _ = w.Write(out)
}

func (s *Server) readImage(r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, s.maxUploadBytes)
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		file, _, err := r.FormFile("image")
		if err != nil {
			return nil, fmt.Errorf("multipart form needs an `image` file field: %w", err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			return nil, fmt.Errorf("reading upload: %w", err)
		}
		return data, nil
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body (max %d MB): %w", s.maxUploadBytes>>20, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty body: send the image bytes, or a multipart form with an `image` field")
	}
	return data, nil
}
