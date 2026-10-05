package upscaler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The binary reports nothing useful for a wrong flag, so the command line is
// pinned here rather than discovered in production.
func TestArgsSpellTheBinarysFlags(t *testing.T) {
	u := New(Options{Model: "realesrgan-x4plus-anime", Scale: 4, ModelsDir: "/m", GPU: "-1", Tile: 128, Threads: "1:2:1"})
	got := strings.Join(u.Args("/in.jpg", "/out.webp", 0), " ")
	want := "-i /in.jpg -o /out.webp -n realesrgan-x4plus-anime -s 4 -m /m -g -1 -t 128 -j 1:2:1 -f webp"
	if got != want {
		t.Fatalf("args\n got %s\nwant %s", got, want)
	}
}

func TestDefaultsLeaveOptionalFlagsOut(t *testing.T) {
	u := New(Options{})
	got := strings.Join(u.Args("in", "out.png", 0), " ")
	if got != "-i in -o out.png -n realesrgan-x4plus -s 2 -f png" {
		t.Fatalf("unexpected args: %s", got)
	}
	// A per-call scale overrides the configured one.
	if got := strings.Join(u.Args("in", "out.png", 4), " "); got != "-i in -o out.png -n realesrgan-x4plus -s 4 -f png" {
		t.Fatalf("scale override: %s", got)
	}
	if u.Options().Timeout != 10*time.Minute {
		t.Errorf("default timeout should be 10m, got %s", u.Options().Timeout)
	}
}

// Bytes round-trips through temp files: what goes in must come back out of
// the file the binary was told to write, and the temp dir must not survive.
func TestBytesRoundTripsThroughTheBinary(t *testing.T) {
	u := New(Options{})
	var seenIn, seenOut string
	u.run = func(_ context.Context, cmd *exec.Cmd) error {
		args := cmd.Args
		for i := range args {
			switch args[i] {
			case "-i":
				seenIn = args[i+1]
			case "-o":
				seenOut = args[i+1]
			}
		}
		data, err := os.ReadFile(seenIn)
		if err != nil {
			return err
		}
		return os.WriteFile(seenOut, append([]byte("UP:"), data...), 0o600)
	}

	out, err := u.Bytes(context.Background(), []byte("pixels"), "png", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "UP:pixels" {
		t.Errorf("got %q", out)
	}
	if _, err := os.Stat(filepath.Dir(seenIn)); !os.IsNotExist(err) {
		t.Errorf("temp dir %s should be gone", filepath.Dir(seenIn))
	}
}

func TestBytesRejectsAFormatTheBinaryCannotWrite(t *testing.T) {
	if _, err := New(Options{}).Bytes(context.Background(), []byte("x"), "gif", 0); err == nil {
		t.Fatal("gif should be refused")
	}
}

func TestAnEmptyOutputIsAFailureNotASuccess(t *testing.T) {
	u := New(Options{})
	u.run = func(_ context.Context, cmd *exec.Cmd) error { return nil } // wrote nothing
	if _, err := u.Bytes(context.Background(), []byte("x"), "png", 0); err == nil {
		t.Fatal("a run that produced no file must fail")
	}
}

func TestCheckNamesTheMissingBinary(t *testing.T) {
	err := New(Options{Binary: "definitely-not-installed-xyz"}).Check()
	if err == nil || !strings.Contains(err.Error(), "definitely-not-installed-xyz") {
		t.Fatalf("want the binary named in the error, got %v", err)
	}
}
