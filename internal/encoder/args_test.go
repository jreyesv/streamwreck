package encoder

import (
	"strings"
	"testing"
	"time"

	"streamwreck/internal/scenario"
)

func baseScenario() *scenario.Scenario {
	return &scenario.Scenario{
		Name:   "t",
		Source: scenario.Source{Type: scenario.SourceTestSrc2, Resolution: "1280x720", FPS: 30, TimecodeOverlay: true},
		Encoder: scenario.Encoder{
			VideoBitrate: 3_000_000, Maxrate: 3_000_000, Bufsize: 6_000_000,
			GOP: 60, KeyintMin: 60, Preset: "veryfast", Tune: "zerolatency", AudioBitrate: 128_000,
		},
		Output: scenario.Output{Protocol: scenario.ProtocolRTMP, URL: "rtmp://ingest/live/stream"},
	}
}

func joinArgs(a []string) string { return strings.Join(a, " ") }

func TestBuildArgs_Baseline(t *testing.T) {
	args, err := BuildArgs(baseScenario(), LaunchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	got := joinArgs(args)
	for _, want := range []string{
		"-f lavfi -i testsrc2=size=1280x720:rate=30",
		"-c:v libx264 -preset veryfast -tune zerolatency",
		"-b:v 3000000 -maxrate 3000000 -bufsize 6000000",
		"-g 60 -keyint_min 60 -sc_threshold 0",
		"-c:a aac -b:a 128000",
		"-f flv rtmp://ingest/live/stream",
		"drawtext=fontfile=",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in argv:\n%s", want, got)
		}
	}
}

func TestBuildArgs_AVDesyncAddsItsoffset(t *testing.T) {
	args, _ := BuildArgs(baseScenario(), LaunchOpts{AVOffset: scenario.Duration(250 * time.Millisecond)})
	got := joinArgs(args)
	if !strings.Contains(got, "-itsoffset 0.25") {
		t.Errorf("expected -itsoffset before audio input:\n%s", got)
	}
	// itsoffset must precede the sine (audio) input it delays.
	if strings.Index(got, "-itsoffset") > strings.Index(got, "sine=") {
		t.Error("itsoffset must come before the audio input")
	}
}

func TestBuildArgs_PTSJumpAddsOffset(t *testing.T) {
	args, _ := BuildArgs(baseScenario(), LaunchOpts{PTSJump: scenario.Duration(5 * time.Second)})
	if !strings.Contains(joinArgs(args), "-output_ts_offset 5") {
		t.Errorf("expected -output_ts_offset for pts_jump")
	}
}

func TestBuildArgs_KeyframeMisalignTrimsFrames(t *testing.T) {
	args, _ := BuildArgs(baseScenario(), LaunchOpts{GOPPhaseShift: 30})
	if !strings.Contains(joinArgs(args), "trim=start_frame=30") {
		t.Errorf("expected trim to shift GOP phase")
	}
}

func TestBuildArgs_ComplexSourceHighEntropy(t *testing.T) {
	s := baseScenario()
	s.Source = scenario.Source{Type: scenario.SourceComplex, Complexity: scenario.ComplexityHigh, Resolution: "1280x720", FPS: 30}
	args, _ := BuildArgs(s, LaunchOpts{})
	got := joinArgs(args)
	if !strings.Contains(got, "geq=random") {
		t.Errorf("high-complexity source should use high-entropy noise:\n%s", got)
	}
}

func TestBuildArgs_SRTOutputUsesMpegts(t *testing.T) {
	s := baseScenario()
	s.Output = scenario.Output{Protocol: scenario.ProtocolSRT, URL: "srt://ingest:8890"}
	args, _ := BuildArgs(s, LaunchOpts{})
	if !strings.Contains(joinArgs(args), "-f mpegts srt://ingest:8890") {
		t.Errorf("SRT output should mux mpegts")
	}
}

func TestBuildArgs_SCTEOverRTMPRejected(t *testing.T) {
	s := baseScenario() // RTMP
	if _, err := BuildArgs(s, LaunchOpts{SCTEFile: "/run/cues.bin"}); err == nil {
		t.Fatal("SCTE mux over RTMP/FLV must be rejected")
	}
}
