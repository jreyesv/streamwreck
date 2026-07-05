package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"streamwreck/internal/scenario"
)

// scaffoldInput is the resolved configuration for a generated scenario, gathered
// from flags and/or interactive prompts.
type scaffoldInput struct {
	Name       string
	Protocol   string // rtmp | srt
	Ingest     string // output.url — the REAL ingest + stream key
	Pull       string // verify.pull — optional viewer playback URL
	Profile    string // clean | flaky-uplink | reconnect
	SourceFile string // optional /media path; empty ⇒ testsrc2
}

// knownProfiles are the starter impairment timelines `init` can scaffold.
var knownProfiles = map[string]bool{"clean": true, "flaky-uplink": true, "reconnect": true}

// prompt asks for a single value, returning def when the user just hits enter.
func prompt(r *bufio.Reader, w io.Writer, label, def string) string {
	if def != "" {
		fmt.Fprintf(w, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(w, "%s: ", label)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// gatherInteractive fills any empty fields of in by prompting on w/r. Ingest is
// required and re-prompts until non-empty.
func gatherInteractive(in scaffoldInput, r *bufio.Reader, w io.Writer) scaffoldInput {
	fmt.Fprintln(w, "Configure a streamwreck scenario for your streaming platform.")
	fmt.Fprintln(w, "Press enter to accept the [default].")
	fmt.Fprintln(w)

	if in.Ingest == "" {
		fmt.Fprintln(w, "Ingest URL — where the encoder PUBLISHES. Point this at your real ingest")
		fmt.Fprintln(w, "and include the stream key, e.g. rtmp://ingest.yourplatform.com/app/STREAMKEY")
		for in.Ingest == "" {
			in.Ingest = prompt(r, w, "  output.url", "")
			if in.Ingest == "" {
				fmt.Fprintln(w, "  (required)")
			}
		}
		fmt.Fprintln(w)
	}
	if in.Protocol == "" {
		in.Protocol = strings.ToLower(prompt(r, w, "Protocol (rtmp|srt)", defaultProtocol(in.Ingest)))
		fmt.Fprintln(w)
	}
	if in.Pull == "" {
		fmt.Fprintln(w, "Playback URL — where the verifier PULLS to grade viewer QoE (join time,")
		fmt.Fprintln(w, "rebuffering, segment/discontinuity checks). Leave blank to skip verification.")
		in.Pull = prompt(r, w, "  verify.pull", "")
		fmt.Fprintln(w)
	}
	if in.Profile == "" {
		fmt.Fprintln(w, "Impairment profile:")
		fmt.Fprintln(w, "  clean         no impairment — connectivity + baseline QoE")
		fmt.Fprintln(w, "  flaky-uplink  delay+jitter+loss burst with a bandwidth cap, then recovery")
		fmt.Fprintln(w, "  reconnect     encoder restart + a kill/respawn (broadcaster reconnects)")
		in.Profile = strings.ToLower(prompt(r, w, "  profile", "flaky-uplink"))
		fmt.Fprintln(w)
	}
	return in
}

func defaultProtocol(ingest string) string {
	if strings.HasPrefix(strings.ToLower(ingest), "srt") {
		return "srt"
	}
	return "rtmp"
}

// scaffoldYAML renders a scenario YAML (with guidance comments) from the input.
func scaffoldYAML(in scaffoldInput) (string, error) {
	if !knownProfiles[in.Profile] {
		return "", fmt.Errorf("unknown profile %q (clean|flaky-uplink|reconnect)", in.Profile)
	}
	proto := in.Protocol
	if proto != "rtmp" && proto != "srt" {
		return "", fmt.Errorf("protocol must be rtmp or srt, got %q", proto)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", in.Name)
	fmt.Fprintf(&b, "description: Uplink/QoE test against %s.\n\n", hostOf(in.Ingest))

	// Source.
	b.WriteString("source:\n")
	if in.SourceFile != "" {
		fmt.Fprintf(&b, "  type: file\n  file: %s          # your own footage (looped)\n", mediaPath(in.SourceFile))
	} else {
		b.WriteString("  type: testsrc2          # swap to `complex` (high motion) or a file via --source-file\n")
	}
	b.WriteString("  resolution: 1280x720\n  fps: 30\n  timecode_overlay: true\n\n")

	// Encoder — sensible live defaults.
	b.WriteString("encoder:\n")
	b.WriteString("  video_bitrate: 6M\n  maxrate: 6M\n  bufsize: 12M\n")
	b.WriteString("  gop: 60\n  keyint_min: 60\n  preset: veryfast\n  tune: zerolatency\n  audio_bitrate: 160k\n\n")

	// Output — THE thing to point at the real platform.
	b.WriteString("# Point this at YOUR real ingest. Include the stream key. Use a STAGING/test\n")
	b.WriteString("# channel — each run creates a real live stream (recordings, notifications, ads).\n")
	b.WriteString("output:\n")
	fmt.Fprintf(&b, "  protocol: %s\n  url: %s\n\n", proto, in.Ingest)

	// Timeline per profile.
	b.WriteString("timeline:\n")
	b.WriteString(profileTimeline(in.Profile))
	b.WriteString("\n")

	// Verify — optional.
	if in.Pull != "" {
		b.WriteString("# Verifier pulls this playback URL and grades viewer QoE.\n")
		b.WriteString("verify:\n  enabled: true\n")
		fmt.Fprintf(&b, "  pull: %s\n", in.Pull)
		b.WriteString("  checks:\n    - join_time\n    - rebuffering\n    - segment_duration\n    - discontinuity_tags\n")
		fmt.Fprintf(&b, "  report: ./reports/%s.json\n", in.Name)
	} else {
		b.WriteString("# No playback URL given — verification is off. Add a `verify:` block with your\n")
		b.WriteString("# viewer HLS URL to grade join time / rebuffering / segment & discontinuity checks.\n")
	}

	// Validate the generated scenario before handing it back.
	if _, err := scenario.Parse([]byte(b.String())); err != nil {
		return "", fmt.Errorf("generated scenario failed validation: %w", err)
	}
	return b.String(), nil
}

// profileTimeline returns the timeline body (indented list items) for a profile.
func profileTimeline(profile string) string {
	switch profile {
	case "clean":
		return "  []  # no impairment; tests connectivity + baseline QoE\n"
	case "reconnect":
		return "" +
			"  - at: 30s\n    action: restart_encoder      # broadcaster reconnect (discontinuity test)\n" +
			"  - at: 60s\n    action: kill_encoder\n    params: { duration: 5s }   # 5s dead, then respawn\n"
	default: // flaky-uplink
		return "" +
			"  - at: 30s\n    network: { delay: 200ms, jitter: 80ms, loss: 10%, rate: 2500kbit, accurate: true }\n" +
			"  - at: 90s\n    network: clear\n"
	}
}

func hostOf(url string) string {
	s := url
	for _, p := range []string{"rtmp://", "rtmps://", "srt://", "http://", "https://"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexAny(s, "/:?"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "your platform"
	}
	return s
}

func mediaPath(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/media/" + p
}
