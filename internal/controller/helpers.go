package controller

import (
	"strings"
	"time"

	"streamwreck/internal/scenario"
)

// gopDurationSeconds is GOP frames divided by source fps.
func gopDurationSeconds(s *scenario.Scenario) float64 {
	fps := s.Source.FPS
	if fps <= 0 {
		fps = 30
	}
	if s.Encoder.GOP <= 0 {
		return 0
	}
	return float64(s.Encoder.GOP) / float64(fps)
}

// timelineDuration is the offset of the last event plus a tail, used to bound
// the authored SCTE schedule.
func timelineDuration(s *scenario.Scenario) scenario.Duration {
	var last time.Duration
	for _, e := range s.Timeline {
		if e.At.Std() > last {
			last = e.At.Std()
		}
	}
	// Give the run a sensible floor so cadence markers exist even for short
	// or event-free scenarios.
	if last < 60*time.Second {
		last = 60 * time.Second
	}
	return scenario.Duration(last + 30*time.Second)
}

// hostFromURL extracts the host from an ingest URL for compact display.
func hostFromURL(u string) string {
	s := u
	for _, p := range []string{"rtmps://", "rtmp://", "srt://", "http://", "https://"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexAny(s, "/:?"); i >= 0 {
		s = s[:i]
	}
	return s
}

// hasNetworkEvents reports whether the timeline applies any uplink shaping, so
// the controller only preflights the shaper when it will actually be used.
func hasNetworkEvents(s *scenario.Scenario) bool {
	for _, e := range s.Timeline {
		if e.Network != nil {
			return true
		}
	}
	return false
}

// expectedDiscontinuities counts events that should produce an
// EXT-X-DISCONTINUITY: encoder restarts, kills, and PTS jumps.
func expectedDiscontinuities(s *scenario.Scenario) int {
	n := 0
	for _, e := range s.Timeline {
		switch e.Action {
		case scenario.ActionRestartEncoder, scenario.ActionKillEncoder, scenario.ActionReconnect, scenario.ActionPTSJump:
			n++
		}
	}
	return n
}

// blackoutSpec is a full egress blackout (100% uplink loss) used by the
// reconnect action to make a disconnect look like lost internet: the ingest sees
// silence — no data, no RTMP unpublish, and (since 100% loss drops it too) not
// even the TCP close — so it engages disconnect protection rather than ending
// the stream.
func blackoutSpec() *scenario.NetworkSpec {
	loss := scenario.Percent(100)
	return &scenario.NetworkSpec{Loss: &loss}
}

// playerImpairment derives the netem spec applied to the player pull path when
// degrade_player is set. Absent a dedicated field, use a moderate default that
// stresses join/rebuffering without killing the pull entirely.
func playerImpairment(s *scenario.Scenario) *scenario.NetworkSpec {
	delay := scenario.Duration(120 * time.Millisecond)
	loss := scenario.Percent(3)
	rate := scenario.Bitrate(1_500_000)
	return &scenario.NetworkSpec{Delay: &delay, Loss: &loss, Rate: &rate}
}

// describeNetwork renders a compact human summary of a network event.
func describeNetwork(n *scenario.NetworkSpec) string {
	if n.Clear {
		return "clear"
	}
	if n.Cut {
		return "cut (link blackout)"
	}
	var parts []string
	if n.Delay != nil {
		d := "delay " + n.Delay.String()
		if n.Jitter != nil {
			d += "±" + n.Jitter.String()
		}
		parts = append(parts, d)
	}
	if n.Loss != nil {
		parts = append(parts, "loss "+n.Loss.TC())
	}
	if n.Rate != nil {
		tag := "rate " + n.Rate.TC()
		if n.Accurate {
			tag += " (htb)"
		}
		parts = append(parts, tag)
	}
	if len(parts) == 0 {
		return "impairment"
	}
	return strings.Join(parts, ", ")
}
