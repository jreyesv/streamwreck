package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"streamwreck/internal/encoder"
)

// ANSI styling, mirroring internal/status and honoring NO_COLOR. Kept local so
// the encoder-log view stays self-contained.
const (
	clReset  = "\033[0m"
	clBold   = "\033[1m"
	clDim    = "\033[2m"
	clRed    = "\033[31m"
	clYellow = "\033[33m"
	clCyan   = "\033[36m"
)

var logColor = os.Getenv("NO_COLOR") == ""

func paint(code, s string) string {
	if !logColor {
		return s
	}
	return code + s + clReset
}

// notable / alarming substrings (lowercased) that likely mark a server-side
// signal or a connection teardown — the lines worth spotting in a disconnect
// investigation. alarming lines are drawn red, notable ones yellow, the rest dim.
var (
	alarmingHints = []string{
		"error", "reset", "broken pipe", "timed out", "timeout", "refused",
		"failed", "cannot", "unable", "denied", "not permitted", "no route",
	}
	notableHints = []string{
		"closing", "closed", "unpublish", "onstatus", "server", "eof",
		"disconnect", "reconnect", "end of stream", "connection",
	}
)

// PrintEncoderLog renders the captured ffmpeg stderr as a titled block with each
// instance separated and interesting lines highlighted. It is meant to run AFTER
// the live dashboard has stopped, so it never competes with the animated view.
func PrintEncoderLog(w io.Writer, instances []encoder.LogInstance) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, paint(clBold+clCyan, "━━━ encoder log "+strings.Repeat("━", 48)))
	fmt.Fprintln(w, paint(clDim, fmt.Sprintf("  %d encoder instance(s) this run. Highlighted lines are likely", len(instances))))
	fmt.Fprintln(w, paint(clDim, "  server-side signals: closes, resets, RTMP status, unpublish."))

	empty := true
	for _, inst := range instances {
		fmt.Fprintln(w)
		fmt.Fprintln(w, paint(clBold, fmt.Sprintf("  ┄┄ instance #%d ┄┄", inst.N)))
		lines := nonEmptyLines(inst.Log)
		if len(lines) == 0 {
			fmt.Fprintln(w, paint(clDim, "    (no output)"))
			continue
		}
		empty = false
		for _, ln := range lines {
			fmt.Fprintf(w, "    %s\n", highlight(ln))
		}
	}
	if empty {
		fmt.Fprintln(w, paint(clDim, "\n  Nothing captured. The encoder may log more at higher verbosity;"))
		fmt.Fprintln(w, paint(clDim, "  ask for a --loglevel bump if you need the RTMP handshake detail."))
	}
	fmt.Fprintln(w, paint(clBold+clCyan, strings.Repeat("━", 64)))
}

// highlight colors a line by how alarming it looks (alarming > notable > plain).
func highlight(line string) string {
	low := strings.ToLower(line)
	for _, h := range alarmingHints {
		if strings.Contains(low, h) {
			return paint(clRed, line)
		}
	}
	for _, h := range notableHints {
		if strings.Contains(low, h) {
			return paint(clYellow, line)
		}
	}
	return paint(clDim, line)
}

func nonEmptyLines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			out = append(out, strings.TrimRight(sc.Text(), "\r"))
		}
	}
	return out
}
