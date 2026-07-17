// Package encoder builds ffmpeg argv from an encoder config and supervises the
// resulting in-container process. Argv construction is pure (no I/O) so it is
// exhaustively unit-tested; supervision lives in supervisor.go.
package encoder

import (
	"fmt"
	"strconv"
	"strings"

	"streamwreck/internal/scenario"
)

// timecodeFont is the in-container path to the font used for the drawtext PTS
// overlay (installed via fonts-dejavu-core in deploy/encoder/Dockerfile).
const timecodeFont = "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"

// LaunchOpts carries the per-launch mutable state that timeline actions mutate
// between (re)starts. A fresh run uses the zero value.
type LaunchOpts struct {
	// AVOffset applies ffmpeg -itsoffset to the audio input, injecting A/V
	// desync (action av_desync).
	AVOffset scenario.Duration
	// PTSJump shifts output timestamps by this amount (action pts_jump),
	// producing a discontinuity the packager must handle.
	PTSJump scenario.Duration
	// GOPPhaseShift delays the first keyframe by N frames so splice points miss
	// IDR frames (action keyframe_misalign). Expressed in frames.
	GOPPhaseShift int
	// SCTEFile, when set, is an in-container path to a stream of SCTE-35 that is
	// muxed into the mpegts output (Phase 5).
	SCTEFile string
	// SourceOverride replaces the scenario source for this launch (source_switch
	// / ad encode).
	SourceOverride *scenario.Source
}

// BuildArgs constructs the full ffmpeg argv (excluding the leading "ffmpeg")
// for the given scenario and launch options.
func BuildArgs(s *scenario.Scenario, opts LaunchOpts) ([]string, error) {
	src := s.Source
	if opts.SourceOverride != nil {
		src = *opts.SourceOverride
	}

	// -nostats drops the per-frame stats from stderr (kept clean for error
	// capture); -progress pipe:1 emits machine-readable metrics on stdout that
	// drive the live dashboard. -loglevel info keeps connection/errors on stderr.
	args := []string{"-hide_banner", "-loglevel", "info", "-nostats", "-progress", "pipe:1", "-re"}

	// Video input.
	vin, err := videoInput(src)
	if err != nil {
		return nil, err
	}
	args = append(args, vin...)

	// Audio input, optionally delayed for A/V desync.
	if opts.AVOffset != 0 {
		args = append(args, "-itsoffset", seconds(opts.AVOffset))
	}
	args = append(args, "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000")

	// Video filter chain: timecode overlay and/or GOP-phase misalignment.
	if vf := videoFilter(src, opts); vf != "" {
		args = append(args, "-vf", vf)
	}

	// Timestamp discontinuity (pts_jump): offset the output clock.
	if opts.PTSJump != 0 {
		args = append(args, "-output_ts_offset", seconds(opts.PTSJump))
	}

	// Encode.
	e := s.Encoder
	args = append(args,
		"-c:v", "libx264",
		"-preset", orDefault(e.Preset, "veryfast"),
	)
	if e.Tune != "" {
		args = append(args, "-tune", e.Tune)
	}
	args = append(args,
		"-b:v", e.VideoBitrate.FFmpeg(),
	)
	if e.Maxrate > 0 {
		args = append(args, "-maxrate", e.Maxrate.FFmpeg())
	}
	if e.Bufsize > 0 {
		args = append(args, "-bufsize", e.Bufsize.FFmpeg())
	}
	// Force an exact GOP: fixed cadence, no scene-cut keyframes.
	args = append(args,
		"-g", strconv.Itoa(e.GOP),
		"-keyint_min", strconv.Itoa(orDefaultInt(e.KeyintMin, e.GOP)),
		"-sc_threshold", "0",
	)
	if e.Resolution != "" {
		args = append(args, "-s", e.Resolution)
	}

	// Audio.
	args = append(args, "-c:a", "aac", "-b:a", orBitrate(e.AudioBitrate, "128k"))

	// SCTE-35 mux (mpegts path only).
	if opts.SCTEFile != "" {
		args = append(args, "-f", "data", "-i", opts.SCTEFile)
	}

	// Output.
	out, err := outputArgs(s.Output, opts.SCTEFile != "")
	if err != nil {
		return nil, err
	}
	args = append(args, out...)
	return args, nil
}

// videoInput returns the -f lavfi -i ... (or -i file) for the source.
func videoInput(src scenario.Source) ([]string, error) {
	res := orDefault(src.Resolution, "1280x720")
	fps := src.FPS
	if fps <= 0 {
		fps = 30
	}
	switch src.Type {
	case scenario.SourceTestSrc2:
		return lavfi(fmt.Sprintf("testsrc2=size=%s:rate=%d", res, fps)), nil
	case scenario.SourceSMPTE:
		return lavfi(fmt.Sprintf("smptebars=size=%s:rate=%d", res, fps)), nil
	case scenario.SourceComplex:
		return lavfi(complexSource(src.Complexity, res, fps)), nil
	case scenario.SourceFile:
		if src.File == "" {
			return nil, fmt.Errorf("source.type=file requires source.file")
		}
		return []string{"-stream_loop", "-1", "-i", src.File}, nil
	default:
		return nil, fmt.Errorf("unsupported source type %q", src.Type)
	}
}

// complexSource generates high-entropy motion that forces VBR bitrate to climb —
// the honest way to produce content-driven spikes (§4.2#5).
func complexSource(c scenario.Complexity, res string, fps int) string {
	switch c {
	case scenario.ComplexityLow:
		return fmt.Sprintf("mandelbrot=size=%s:rate=%d", res, fps)
	case scenario.ComplexityMedium:
		// Zooming mandelbrot: continuous fine detail change.
		return fmt.Sprintf("mandelbrot=size=%s:rate=%d:maxiter=1000", res, fps)
	default: // high
		// Full-frame animated noise: maximal entropy, defeats inter-prediction.
		return fmt.Sprintf("nullsrc=size=%s:rate=%d,geq=random(1)*255:128:128", res, fps)
	}
}

// videoFilter composes the drawtext timecode overlay and any GOP-phase shift.
func videoFilter(src scenario.Source, opts LaunchOpts) string {
	var chain []string
	// keyframe_misalign: trim the leading N frames so the IDR cadence shifts
	// phase relative to the SCTE splice schedule.
	if opts.GOPPhaseShift > 0 {
		chain = append(chain, fmt.Sprintf("trim=start_frame=%d,setpts=PTS-STARTPTS", opts.GOPPhaseShift))
	}
	if src.TimecodeOverlay {
		// Explicit fontfile so drawtext works regardless of fontconfig config in
		// the image (DejaVu ships via fonts-dejavu-core in the encoder image).
		chain = append(chain, "drawtext=fontfile="+timecodeFont+":text='%{pts\\:hms}':"+
			"fontsize=48:x=10:y=10:box=1:boxcolor=black:fontcolor=white")
	}
	return strings.Join(chain, ",")
}

// outputArgs renders the muxer + destination. mpegts is required whenever SCTE
// is muxed (FLV cannot carry SCTE-35); otherwise FLV for RTMP, mpegts for SRT.
func outputArgs(o scenario.Output, scteMuxed bool) ([]string, error) {
	switch o.Protocol {
	case scenario.ProtocolRTMP:
		if scteMuxed {
			return nil, fmt.Errorf("SCTE-35 mux requires SRT output; RTMP/FLV cannot carry it")
		}
		return []string{"-f", "flv", o.URL}, nil
	case scenario.ProtocolSRT:
		args := []string{"-f", "mpegts"}
		if scteMuxed {
			// -copyts keeps the authored splice PTS aligned with the program.
			args = append(args, "-copyts")
		}
		return append(args, o.URL), nil
	default:
		return nil, fmt.Errorf("unsupported output protocol %q", o.Protocol)
	}
}

func lavfi(desc string) []string { return []string{"-f", "lavfi", "-i", desc} }

func seconds(d scenario.Duration) string {
	return strconv.FormatFloat(d.Std().Seconds(), 'f', -1, 64)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDefaultInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func orBitrate(b scenario.Bitrate, def string) string {
	if b <= 0 {
		return def
	}
	return b.FFmpeg()
}
