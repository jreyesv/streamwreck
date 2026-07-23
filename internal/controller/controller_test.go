package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"streamwreck/internal/run"
	"streamwreck/internal/scenario"
)

// buildScenario returns a minimal valid scenario with all events at t=0 so the
// timeline stepper fires immediately (keeps the test fast).
func buildScenario(t *testing.T, yaml string) *scenario.Scenario {
	t.Helper()
	s, err := scenario.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

func joinedArgv(c run.Call) string {
	return c.Service + ": " + strings.Join(c.Argv, " ")
}

func TestRun_LaunchesEncoderAndAppliesNetwork(t *testing.T) {
	s := buildScenario(t, `
name: t
source: { type: testsrc2, fps: 30 }
encoder: { video_bitrate: 3M, gop: 60 }
output: { protocol: rtmp, url: rtmp://ingest/live/stream }
timeline:
  - at: 0s
    network: { delay: 40ms, loss: 1% }
`)
	fake := run.NewFake()
	c := New(fake)
	c.log = func(string, ...any) {}
	zero := time.Duration(0)
	c.runDurOverride = &zero
	c.encoderGrace = 0
	c.stopGrace = 0
	c.noUI = true

	if _, err := c.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	var sawEncoderLaunch, sawNetem bool
	for _, call := range fake.ExecCalls() {
		j := joinedArgv(call)
		if call.Service == svcEncoder && strings.Contains(j, "ffmpeg") {
			sawEncoderLaunch = true
		}
		if call.Service == svcShaper && strings.Contains(j, "netem delay 40ms") {
			sawNetem = true
		}
	}
	if !sawEncoderLaunch {
		t.Error("expected the encoder ffmpeg to be launched in the encoder service")
	}
	if !sawNetem {
		t.Error("expected netem impairment applied in the shaper service")
	}
}

func TestRun_RestartEncoderRelaunches(t *testing.T) {
	s := buildScenario(t, `
name: t
source: { type: testsrc2, fps: 30 }
encoder: { video_bitrate: 3M, gop: 60 }
output: { protocol: rtmp, url: rtmp://ingest/live/stream }
timeline:
  - at: 0s
    action: restart_encoder
`)
	fake := run.NewFake()
	c := New(fake)
	c.log = func(string, ...any) {}
	zero := time.Duration(0)
	c.runDurOverride = &zero
	c.encoderGrace = 0
	c.stopGrace = 0
	c.noUI = true
	if _, err := c.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	launches := 0
	for _, call := range fake.ExecCalls() {
		if call.Service == svcEncoder && strings.Contains(strings.Join(call.Argv, " "), "ffmpeg ") {
			launches++
		}
	}
	// Initial launch + the restart = at least 2 ffmpeg starts.
	if launches < 2 {
		t.Errorf("expected >=2 encoder launches (initial + restart), got %d", launches)
	}
}

func TestRun_ReconnectBlacksOutAndRelaunches(t *testing.T) {
	s := buildScenario(t, `
name: t
source: { type: testsrc2, fps: 30 }
encoder: { video_bitrate: 3M, gop: 60 }
output: { protocol: rtmp, url: rtmp://ingest/live/stream }
timeline:
  - at: 0s
    action: reconnect
    params: { duration: 0s }
`)
	fake := run.NewFake()
	c := New(fake)
	c.log = func(string, ...any) {}
	zero := time.Duration(0)
	c.runDurOverride = &zero
	c.encoderGrace = 0
	c.stopGrace = 0
	c.noUI = true
	if _, err := c.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	var sawBlackout bool
	launches := 0
	for _, call := range fake.ExecCalls() {
		j := joinedArgv(call)
		if call.Service == svcShaper && strings.Contains(j, "netem loss 100%") {
			sawBlackout = true
		}
		if call.Service == svcEncoder && strings.Contains(j, "ffmpeg ") {
			launches++
		}
	}
	if !sawBlackout {
		t.Error("reconnect should blackout the uplink egress (netem loss 100%) so the drop looks like lost internet")
	}
	// Initial launch + the reconnect relaunch = at least 2 ffmpeg starts.
	if launches < 2 {
		t.Errorf("expected >=2 encoder launches (initial + reconnect), got %d", launches)
	}
}

func TestExpectedDiscontinuities(t *testing.T) {
	s := buildScenario(t, `
name: t
source: { type: testsrc2, fps: 30 }
encoder: { video_bitrate: 3M, gop: 60 }
output: { protocol: rtmp, url: rtmp://ingest/live/stream }
timeline:
  - at: 0s
    action: restart_encoder
  - at: 1s
    action: pts_jump
    params: { jump: 5s }
  - at: 2s
    network: clear
`)
	if got := expectedDiscontinuities(s); got != 2 {
		t.Errorf("expected 2 discontinuity-producing events, got %d", got)
	}
}
