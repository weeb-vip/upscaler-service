package server

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/weeb-vip/upscaler-service/internal/upscaler"
)

// An upscaler whose binary is a fake that prefixes the input, so the HTTP
// layer can be exercised without Real-ESRGAN installed.
func fakeUpscaler(t *testing.T) *upscaler.Upscaler {
	t.Helper()
	u := upscaler.New(upscaler.Options{})
	upscaler.SetRunner(u, func(_ context.Context, cmd *exec.Cmd) error {
		var in, out string
		for i, a := range cmd.Args {
			if a == "-i" {
				in = cmd.Args[i+1]
			}
			if a == "-o" {
				out = cmd.Args[i+1]
			}
		}
		data, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		return os.WriteFile(out, append([]byte("UP:"), data...), 0o600)
	})
	return u
}

func TestRawBodyComesBackUpscaledAsTheRequestedFormat(t *testing.T) {
	srv := httptest.NewServer(New(fakeUpscaler(t), "png", 1<<20).Handler())
	defer srv.Close()

	res, err := http.Post(srv.URL+"/upscale?format=webp", "image/jpeg", bytes.NewBufferString("pixels"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/webp" {
		t.Errorf("content type %q", ct)
	}
	var buf bytes.Buffer
	buf.ReadFrom(res.Body)
	if buf.String() != "UP:pixels" {
		t.Errorf("body %q", buf.String())
	}
}

func TestMultipartImageFieldIsAccepted(t *testing.T) {
	srv := httptest.NewServer(New(fakeUpscaler(t), "png", 1<<20).Handler())
	defer srv.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("image", "poster.jpg")
	fw.Write([]byte("pixels"))
	mw.Close()

	res, err := http.Post(srv.URL+"/upscale", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("default format should be the service's (png), got %q", ct)
	}
}

func TestBadRequests(t *testing.T) {
	srv := httptest.NewServer(New(fakeUpscaler(t), "png", 16).Handler())
	defer srv.Close()

	cases := map[string]struct {
		url, body string
		want      int
	}{
		"unknown format": {"/upscale?format=gif", "pixels", 400},
		"empty body":     {"/upscale", "", 400},
		"too large":      {"/upscale", "this body is longer than sixteen bytes", 400},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := http.Post(srv.URL+c.url, "image/png", bytes.NewBufferString(c.body))
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != c.want {
				t.Errorf("status %d, want %d", res.StatusCode, c.want)
			}
		})
	}
}

func TestHealthReportsTheMissingBinary(t *testing.T) {
	u := upscaler.New(upscaler.Options{Binary: "no-such-binary-abc"})
	srv := httptest.NewServer(New(u, "png", 1<<20).Handler())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 503 {
		t.Errorf("status %d, want 503 while the binary is missing", res.StatusCode)
	}
}
