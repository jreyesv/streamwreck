// Package verify pulls the resulting HLS manifest and compares reality against
// the scenario's intent, emitting a report.Report. This is what makes
// streamwreck a test tool rather than a chaos generator (spec §10).
package verify

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"streamwreck/internal/report"
	"streamwreck/internal/run"
	"streamwreck/internal/scenario"
)

// Expectations carries what the controller knows about intent, for checks that
// diff observed-vs-authored.
type Expectations struct {
	GOPDurationSeconds float64         // GOP frames / fps
	Discontinuities    int             // count of restart/pts_jump events
	AuthoredSCTE       []report.Marker // authored markers, if scte enabled
	PlaybackWindow     time.Duration   // how long to observe for rebuffering
}

// Verifier runs the checks against a live pull URL.
type Verifier struct {
	runner        run.Runner
	playerService string
	fetch         func(ctx context.Context, url string) (string, error)
}

// New builds a Verifier. The player service runs ffmpeg pulls for join/rebuffer
// measurement; manifest fetching goes through the player container too so it
// sees the (optionally impaired) pull path.
func New(r run.Runner, playerService string) *Verifier {
	v := &Verifier{runner: r, playerService: playerService}
	v.fetch = v.fetchViaPlayer
	return v
}

// Run executes every enabled check and returns a populated report.
func (v *Verifier) Run(ctx context.Context, s *scenario.Scenario, exp Expectations) (*report.Report, error) {
	rep := &report.Report{Scenario: s.Name, StartedAt: time.Now(), Pass: true}

	manifest, err := v.pullMedia(ctx, s.Verify.Pull)
	if err != nil {
		rep.Add("manifest_fetch", false, "could not pull manifest: %v", err)
		return rep, nil // a failed fetch is a failed run, not a Go error
	}
	rep.Discontinuities = manifest.DiscontinuityCount()

	for _, check := range s.Verify.Checks {
		switch check {
		case "segment_duration":
			v.checkSegmentDuration(rep, manifest, exp.GOPDurationSeconds)
		case "discontinuity_tags":
			v.checkDiscontinuities(rep, manifest, exp.Discontinuities)
		case "scte_markers":
			v.checkSCTE(rep, manifest, exp.AuthoredSCTE)
		case "join_time":
			v.checkJoinTime(ctx, rep, s.Verify.Pull)
		case "rebuffering":
			v.checkRebuffering(ctx, rep, s.Verify.Pull, exp.PlaybackWindow)
		}
	}

	rep.Duration = time.Since(rep.StartedAt).Round(time.Millisecond).String()
	return rep, nil
}

// pullMedia fetches the manifest, following a master playlist to its first
// variant so callers always get a media playlist.
func (v *Verifier) pullMedia(ctx context.Context, url string) (*MediaPlaylist, error) {
	text, err := v.fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	if IsMaster(text) {
		variant := FirstVariantURI(text)
		if variant == "" {
			return nil, fmt.Errorf("master playlist has no variants")
		}
		text, err = v.fetch(ctx, resolveURL(url, variant))
		if err != nil {
			return nil, err
		}
	}
	return ParseMediaPlaylist(text), nil
}

// checkSegmentDuration: every EXTINF should be a multiple of the GOP duration.
func (v *Verifier) checkSegmentDuration(rep *report.Report, mp *MediaPlaylist, gop float64) {
	if gop <= 0 {
		rep.Add("segment_duration", false, "GOP duration unknown")
		return
	}
	var offenders []string
	for _, seg := range mp.Segments {
		if seg.Duration == 0 {
			continue
		}
		ratio := seg.Duration / gop
		if math.Abs(ratio-math.Round(ratio)) > 0.05 { // 5% tolerance
			offenders = append(offenders, fmt.Sprintf("%.3fs", seg.Duration))
		}
	}
	if len(offenders) > 0 {
		rep.Add("segment_duration", false,
			"%d/%d segments not a multiple of %.3fs GOP: %s",
			len(offenders), len(mp.Segments), gop, strings.Join(offenders, ", "))
		return
	}
	rep.Add("segment_duration", true, "all %d segments align to %.3fs GOP", len(mp.Segments), gop)
}

// checkDiscontinuities: EXT-X-DISCONTINUITY count should equal the expected
// number of restart/pts_jump events.
func (v *Verifier) checkDiscontinuities(rep *report.Report, mp *MediaPlaylist, expected int) {
	got := mp.DiscontinuityCount()
	pass := got == expected
	rep.Add("discontinuity_tags", pass, "expected %d discontinuities, observed %d", expected, got)
}

// checkSCTE diffs observed manifest markers against authored ones.
func (v *Verifier) checkSCTE(rep *report.Report, mp *MediaPlaylist, authored []report.Marker) {
	observed := extractMarkers(mp)
	diff := report.SCTEDiff{Authored: authored, Observed: observed}
	diff.Matched = min(len(authored), len(observed))
	diff.Missing = max(0, len(authored)-len(observed))
	diff.Extra = max(0, len(observed)-len(authored))
	rep.SCTE = &diff
	pass := diff.Missing == 0 && diff.Extra == 0
	rep.Add("scte_markers", pass, "authored %d, observed %d (missing %d, extra %d)",
		len(authored), len(observed), diff.Missing, diff.Extra)
}

// extractMarkers counts SCTE markers surfaced in the manifest. A CUE-OUT marks
// the start of a break; standalone DATERANGE tags (not co-located with a CUE-OUT
// segment) are counted too. Timing/type refinement (threefive decode of the
// embedded cue) is layered on in the scte package.
func extractMarkers(mp *MediaPlaylist) []report.Marker {
	var out []report.Marker
	for _, seg := range mp.Segments {
		if seg.CueOut {
			out = append(out, report.Marker{Type: "time_signal"})
		}
	}
	// DATERANGE tags that carry SCTE cues but did not coincide with a CUE-OUT
	// segment (some packagers emit only DATERANGE).
	if len(out) == 0 {
		for range mp.DateRanges {
			out = append(out, report.Marker{Type: "time_signal"})
		}
	}
	return out
}

func resolveURL(base, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if i := strings.LastIndex(base, "/"); i >= 0 {
		return base[:i+1] + ref
	}
	return ref
}

// atoiDefault is a tiny helper for parsing progress fields.
func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
