package status

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestParseProgress(t *testing.T) {
	in := strings.Join([]string{
		"frame=30", "fps=30.0", "bitrate=3000.0kbits/s", "out_time=00:00:01.000000",
		"speed=1.00x", "drop_frames=0", "progress=continue",
		"frame=60", "fps=29.5", "bitrate=6000.0kbits/s", "out_time=00:00:02.000000",
		"speed=0.90x", "drop_frames=4", "progress=continue",
	}, "\n")

	var updates []Metrics
	ParseProgress(strings.NewReader(in), func(m Metrics) { updates = append(updates, m) })

	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
	last := updates[1]
	if last.Frames != 60 || last.BitrateKbps != 6000 || last.Speed != 0.90 || last.Dropped != 4 {
		t.Errorf("last update wrong: %+v", last)
	}
	if last.OutTime != 2*time.Second {
		t.Errorf("out_time = %v, want 2s", last.OutTime)
	}
	if !last.Valid {
		t.Error("update should be marked valid")
	}
}

func TestParseProgress_HandlesNA(t *testing.T) {
	in := "bitrate=N/A\nspeed=N/A\nprogress=continue\n"
	var got Metrics
	ParseProgress(strings.NewReader(in), func(m Metrics) { got = m })
	if got.BitrateKbps != 0 || got.Speed != 0 {
		t.Errorf("N/A should parse to 0, got %+v", got)
	}
}

var ansi = regexp.MustCompile(`\033\[[0-9;?]*[a-zA-Z]`)

func strip(s string) string { return ansi.ReplaceAllString(s, "") }

func TestModelRender_ShowsStreamFacts(t *testing.T) {
	m := NewModel("flaky-wifi", "1280x720", 30, 60, 3000, "rtmp", "ingest.example.tv", 90*time.Second)
	m.SetMetrics(Metrics{Valid: true, BitrateKbps: 3000, FPS: 29.9, Speed: 1.0, Frames: 100})
	m.SetNetwork("delay 200ms, loss 15%")

	out := strip(strings.Join(m.lines(false), "\n"))
	for _, want := range []string{
		"streamwreck", "flaky-wifi",
		"1280x720", "30fps", "keyframe 2.0s", "3.0 Mbps", "rtmp",
		"3000 kbps", "29.9fps", "100 frames",
		"delay 200ms, loss 15%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

func TestModelRender_StarvationShowsLowPercent(t *testing.T) {
	m := NewModel("starve", "1280x720", 30, 60, 6000, "rtmp", "h", 90*time.Second)
	m.SetMetrics(Metrics{Valid: true, BitrateKbps: 1500, FPS: 10, Speed: 0.3})
	out := strip(strings.Join(m.lines(false), "\n"))
	if !strings.Contains(out, "25%") { // 1500/6000
		t.Errorf("expected 25%% bitrate-of-target, got:\n%s", out)
	}
}

func TestPlainLine(t *testing.T) {
	m := NewModel("t", "1280x720", 30, 60, 3000, "rtmp", "h", 90*time.Second)
	m.SetMetrics(Metrics{Valid: true, BitrateKbps: 3200, FPS: 30, Speed: 1.0})
	line := m.plainLine()
	if !strings.Contains(line, "3200kbps") || !strings.Contains(line, "[streamwreck]") {
		t.Errorf("plain line unexpected: %q", line)
	}
}
