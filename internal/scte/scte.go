// Package scte authors SCTE-35 markers on a cadence and decodes markers for
// verification. The schedule computation is pure Go (unit-tested); marker
// binary authoring and manifest-cue decoding shell out to threefive inside the
// shaper sidecar.
//
// Muxing path (spec §12, open decision): markers are authored as a timed cue
// file and muxed into the mpegts (SRT) output. FLV/RTMP cannot carry SCTE-35,
// so scte requires output.protocol=srt (enforced in scenario validation). The
// alternative MediaMTX-side authoring path is left as a documented substitution
// point — see Author.
package scte

import (
	"context"
	"fmt"
	"math"
	"strings"

	"streamwreck/internal/report"
	"streamwreck/internal/run"
	"streamwreck/internal/scenario"
)

// Schedule computes the authored markers for a scenario over a run of the given
// duration. Each marker fires every cadence; its splice PTS is `preroll` in the
// future from the fire time. on_keyframe reflects whether the splice lands on a
// GOP boundary — false by construction when misalign is set.
func Schedule(s *scenario.Scenario, gopDurationSeconds float64, runDuration scenario.Duration) []report.Marker {
	if s.SCTE == nil || !s.SCTE.Enabled {
		return nil
	}
	cadence := s.SCTE.Cadence.Std().Seconds()
	if cadence <= 0 {
		return nil
	}
	preroll := s.SCTE.Preroll.Std().Seconds()
	total := runDuration.Std().Seconds()

	var out []report.Marker
	for fire := cadence; fire < total; fire += cadence {
		splicePTS := fire + preroll
		if s.SCTE.Misalign && gopDurationSeconds > 0 {
			// Push the splice half a GOP off the keyframe grid.
			splicePTS += gopDurationSeconds / 2
		}
		out = append(out, report.Marker{
			Type:       s.SCTE.Type,
			PTSSeconds: splicePTS,
			OnKeyframe: !s.SCTE.Misalign && landsOnKeyframe(splicePTS, gopDurationSeconds),
		})
	}
	return out
}

// landsOnKeyframe reports whether a PTS coincides with a GOP boundary.
func landsOnKeyframe(pts, gop float64) bool {
	if gop <= 0 {
		return false
	}
	frac := math.Mod(pts, gop)
	return frac < 0.02 || gop-frac < 0.02 // 20ms tolerance
}

// Authorer authors and decodes SCTE-35 via threefive in a sidecar service.
type Authorer struct {
	runner  run.Runner
	service string
}

func New(r run.Runner, service string) *Authorer {
	return &Authorer{runner: r, service: service}
}

// AuthorCue returns a base64 SCTE-35 cue for a single splice point, built with
// threefive. typ is "time_signal" or "splice_insert".
func (a *Authorer) AuthorCue(ctx context.Context, typ string, ptsSeconds, breakSeconds float64) (string, error) {
	py := buildAuthorScript(typ, ptsSeconds, breakSeconds)
	out, err := a.runner.Exec(ctx, a.service, "python3", "-c", py)
	if err != nil {
		return "", fmt.Errorf("threefive author: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// Decode decodes a base64/hex SCTE-35 cue (e.g. extracted from a manifest tag)
// via threefive and returns the decoded JSON for the verifier to compare.
func (a *Authorer) Decode(ctx context.Context, cue string) (string, error) {
	py := fmt.Sprintf(
		"import threefive,json; c=threefive.Cue(%q); c.decode(); print(json.dumps(c.get()))", cue)
	out, err := a.runner.Exec(ctx, a.service, "python3", "-c", py)
	if err != nil {
		return "", fmt.Errorf("threefive decode: %w", err)
	}
	return out, nil
}

// buildAuthorScript renders a threefive python snippet that emits a base64 cue.
// threefive (3.x) provides mk_time_signal(pts) and
// mk_splice_insert(event_id, pts, duration, out) factories that build a fully
// encodable Cue; .encode() returns the base64 string.
func buildAuthorScript(typ string, ptsSeconds, breakSeconds float64) string {
	// PTS in threefive is in seconds (it converts to the 90kHz clock internally).
	switch typ {
	case "splice_insert":
		return fmt.Sprintf(
			"import threefive; print(threefive.mk_splice_insert(1, %.6f, %.6f, True).encode())",
			ptsSeconds, breakSeconds)
	default: // time_signal
		return fmt.Sprintf(
			"import threefive; print(threefive.mk_time_signal(%.6f).encode())",
			ptsSeconds)
	}
}
