package verify

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"streamwreck/internal/report"
)

// fetchViaPlayer GETs a URL from inside the player container (so it traverses
// the — possibly impaired — pull path). The player image ships curl.
//
// -L follows MediaMTX's HLS redirect and -c/-b share a cookie jar so its
// cookieCheck round-trip completes (the first request 302s to set a cookie).
func (v *Verifier) fetchViaPlayer(ctx context.Context, url string) (string, error) {
	out, err := v.runner.Exec(ctx, v.playerService, "curl", "-fsSL",
		"-c", "/tmp/sw-cookies", "-b", "/tmp/sw-cookies", url)
	if err != nil {
		return "", err
	}
	return out, nil
}

// checkJoinTime measures wall-clock time from player start to the first decoded
// frame — a lightweight proxy for time-to-first-frame (§10). OCR of the
// burned-in timecode is the heavier, more precise upgrade path (§12).
func (v *Verifier) checkJoinTime(ctx context.Context, rep *report.Report, url string) {
	script := fmt.Sprintf(
		"start=$(date +%%s%%3N); "+
			"ffmpeg -hide_banner -loglevel error -i '%s' -frames:v 1 -f null - >/dev/null 2>&1; "+
			"end=$(date +%%s%%3N); echo $((end-start))", url)
	out, err := v.runner.Exec(ctx, v.playerService, "sh", "-c", script)
	if err != nil {
		rep.Add("join_time", false, "join failed: %v", err)
		return
	}
	ms, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if perr != nil {
		rep.Add("join_time", false, "could not measure join time: %q", strings.TrimSpace(out))
		return
	}
	rep.JoinTimeMS = &ms
	// A join is "healthy" if first frame arrives within a generous ceiling.
	pass := ms < 10_000
	rep.Add("join_time", pass, "%d ms to first frame", ms)
}

var frameRe = regexp.MustCompile(`frame=\s*(\d+)`)

// checkRebuffering pulls the stream for a fixed window and estimates stalls from
// the decoded-frame deficit versus wall-clock expectation. Coarse but
// deterministic; the plan flags OCR as the precise upgrade if this proves too
// blunt.
func (v *Verifier) checkRebuffering(ctx context.Context, rep *report.Report, url string, window time.Duration) {
	if window <= 0 {
		window = 20 * time.Second
	}
	secs := int(window.Seconds())
	// -re makes ffmpeg read at realtime so a stall shows up as a frame deficit.
	script := fmt.Sprintf(
		"ffmpeg -hide_banner -re -i '%s' -t %d -f null - 2>&1 | tail -c 4000", url, secs)
	out, err := v.runner.Exec(ctx, v.playerService, "sh", "-c", script)
	if err != nil {
		// ffmpeg may exit non-zero at stream end; still parse what we got.
		out = err.Error()
	}
	got := lastFrameCount(out)
	// Expected frames ≈ window * fps; fps is unknown here, use the manifest-
	// independent lower bound of 24fps to avoid false positives.
	expected := secs * 24
	deficit := expected - got
	count := 0
	var stallMS int64
	if got > 0 && deficit > int(float64(expected)*0.1) { // >10% deficit ⇒ stall
		count = 1
		stallMS = int64(float64(deficit) / 24.0 * 1000)
	}
	rep.RebufferCount = &count
	rep.RebufferMS = &stallMS
	pass := count == 0
	rep.Add("rebuffering", pass, "%d stalls (~%d ms), decoded %d frames over %ds window",
		count, stallMS, got, secs)
}

func lastFrameCount(ffmpegOut string) int {
	matches := frameRe.FindAllStringSubmatch(ffmpegOut, -1)
	if len(matches) == 0 {
		return 0
	}
	return atoiDefault(matches[len(matches)-1][1], 0)
}
