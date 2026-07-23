package main

import (
	"os"
	"strings"
	"testing"

	"streamwreck/internal/encoder"
)

func TestPrintEncoderLog_StructureAndHighlight(t *testing.T) {
	logColor = false // deterministic, no ANSI in assertions
	var b strings.Builder
	PrintEncoderLog(&b, []encoder.LogInstance{
		{N: 1, Log: "Opening 'rtmps://ingest' for writing\nStream mapping ok\nConnection reset by peer"},
		{N: 2, Log: "Opening 'rtmps://ingest' for writing\n"},
	})
	out := b.String()
	for _, want := range []string{"encoder log", "instance #1", "instance #2", "Connection reset by peer"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// Visual check: `go test ./cmd/streamwreck -run RenderEncoderLog -v` prints a
// colored sample so the styling can be eyeballed. Skipped in normal runs.
func TestRenderEncoderLog(t *testing.T) {
	if os.Getenv("SHOW_ENCODER_LOG") == "" {
		t.Skip("set SHOW_ENCODER_LOG=1 to render a sample")
	}
	logColor = true
	PrintEncoderLog(os.Stdout, []encoder.LogInstance{
		{N: 1, Log: "[flv @ 0x..] Opening 'rtmps://ingest/app/key' for writing\n" +
			"[rtmp @ 0x..] Handshake completed\nframe= 900 fps=30\n" +
			"[flv @ 0x..] Failed to update header with correct duration\n" +
			"av_interleaved_write_frame(): Broken pipe\n" +
			"[rtmp @ 0x..] Server error: Unpublish notify received\nConnection reset by peer"},
		{N: 2, Log: "[flv @ 0x..] Opening 'rtmps://ingest/app/key' for writing\n" +
			"[rtmp @ 0x..] Handshake completed\nframe= 120 fps=30 speed=1.0x"},
	})
}
