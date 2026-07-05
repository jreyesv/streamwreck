package scenario

import (
	"testing"
	"time"
)

func TestParseBitrate(t *testing.T) {
	cases := map[string]int64{
		"3M":       3_000_000,
		"800kbit":  800_000,
		"128k":     128_000,
		"500":      500,
		"2500kbit": 2_500_000,
		"1g":       1_000_000_000,
	}
	for in, want := range cases {
		got, err := ParseBitrate(in)
		if err != nil {
			t.Fatalf("ParseBitrate(%q): %v", in, err)
		}
		if got.BitsPerSecond() != want {
			t.Errorf("ParseBitrate(%q) = %d, want %d", in, got.BitsPerSecond(), want)
		}
	}
	if _, err := ParseBitrate("notarate"); err == nil {
		t.Error("expected error for invalid bitrate")
	}
}

func TestBitrateRendering(t *testing.T) {
	b := Bitrate(800_000)
	if b.TC() != "800kbit" {
		t.Errorf("TC() = %q, want 800kbit", b.TC())
	}
	if b.FFmpeg() != "800000" {
		t.Errorf("FFmpeg() = %q, want 800000", b.FFmpeg())
	}
}

func TestParsePercent(t *testing.T) {
	cases := map[string]float64{"0.5%": 0.5, "15%": 15, "3": 3}
	for in, want := range cases {
		got, err := ParsePercent(in)
		if err != nil {
			t.Fatalf("ParsePercent(%q): %v", in, err)
		}
		if got.Value() != want {
			t.Errorf("ParsePercent(%q) = %v, want %v", in, got.Value(), want)
		}
	}
}

func TestParseScenario_NetworkClearVsObject(t *testing.T) {
	y := `
name: t
source: { type: testsrc2, fps: 30 }
encoder: { video_bitrate: 3M, gop: 60 }
output: { protocol: rtmp, url: rtmp://x/y }
timeline:
  - at: 0s
    network: { delay: 40ms, jitter: 10ms, loss: 0.5% }
  - at: 30s
    network: clear
`
	s, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Timeline) != 2 {
		t.Fatalf("got %d events", len(s.Timeline))
	}
	first := s.Timeline[0].Network
	if first == nil || first.Clear {
		t.Fatal("first event should be an object impairment, not clear")
	}
	if first.Delay.Std() != 40*time.Millisecond {
		t.Errorf("delay = %v, want 40ms", first.Delay.Std())
	}
	if first.Loss.Value() != 0.5 {
		t.Errorf("loss = %v, want 0.5", first.Loss.Value())
	}
	if second := s.Timeline[1].Network; second == nil || !second.Clear {
		t.Error("second event should be network: clear")
	}
}

func TestParseScenario_TimelineSorted(t *testing.T) {
	y := `
name: t
source: { type: testsrc2, fps: 30 }
encoder: { video_bitrate: 3M, gop: 60 }
output: { protocol: rtmp, url: rtmp://x/y }
timeline:
  - at: 60s
    network: clear
  - at: 10s
    network: clear
  - at: 30s
    network: clear
`
	s, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}
	for i, w := range want {
		if s.Timeline[i].At.Std() != w {
			t.Errorf("event[%d].At = %v, want %v", i, s.Timeline[i].At.Std(), w)
		}
	}
}

func TestValidate_RejectsMultipleDirectives(t *testing.T) {
	y := `
name: t
source: { type: testsrc2, fps: 30 }
encoder: { video_bitrate: 3M, gop: 60 }
output: { protocol: rtmp, url: rtmp://x/y }
timeline:
  - at: 0s
    network: clear
    action: restart_encoder
`
	if _, err := Parse([]byte(y)); err == nil {
		t.Fatal("expected validation error for two directives on one event")
	}
}

func TestValidate_ActionParamsRequired(t *testing.T) {
	y := `
name: t
source: { type: testsrc2, fps: 30 }
encoder: { video_bitrate: 3M, gop: 60 }
output: { protocol: rtmp, url: rtmp://x/y }
timeline:
  - at: 0s
    action: av_desync
`
	if _, err := Parse([]byte(y)); err == nil {
		t.Fatal("expected error: av_desync requires params.offset")
	}
}

func TestValidate_SCTERequiresSRT(t *testing.T) {
	y := `
name: t
source: { type: testsrc2, fps: 30 }
encoder: { video_bitrate: 3M, gop: 60 }
output: { protocol: rtmp, url: rtmp://x/y }
timeline: []
scte35: { enabled: true, type: time_signal, cadence: 30s }
`
	if _, err := Parse([]byte(y)); err == nil {
		t.Fatal("expected error: SCTE over RTMP should be rejected")
	}
}

func TestParse_RejectsUnknownField(t *testing.T) {
	y := `
name: t
source: { type: testsrc2, fps: 30 }
encoder: { video_bitrate: 3M, gop: 60 }
output: { protocol: rtmp, url: rtmp://x/y }
timeline: []
bogus_top_level: 42
`
	if _, err := Parse([]byte(y)); err == nil {
		t.Fatal("expected error for unknown top-level field")
	}
}
