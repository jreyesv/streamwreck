// Package status renders a live, in-place terminal dashboard while a stream is
// running: static stream facts (resolution, fps, keyframe interval, target
// bitrate) alongside live metrics (bitrate, fps, speed, dropped frames) parsed
// from ffmpeg's machine-readable -progress output, plus the current network
// impairment. It degrades to periodic plain lines when stdout isn't a TTY.
package status

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
)

// Metrics is one snapshot of ffmpeg -progress output.
type Metrics struct {
	Frames      int
	FPS         float64
	BitrateKbps float64
	Speed       float64       // realtime multiple; 1.0 = keeping up
	OutTime     time.Duration // encoded media time so far
	Dropped     int
	Valid       bool // false until the first real update
}

// ParseProgress reads ffmpeg `-progress` key=value blocks from r and calls
// onUpdate once per block (blocks are delimited by a `progress=` line). It
// returns when r reaches EOF (i.e. ffmpeg exits).
func ParseProgress(r io.Reader, onUpdate func(Metrics)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var m Metrics
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "frame":
			m.Frames = atoi(val)
		case "fps":
			m.FPS = atof(val)
		case "bitrate":
			m.BitrateKbps = parseKbits(val)
		case "drop_frames":
			m.Dropped = atoi(val)
		case "speed":
			m.Speed = parseSpeed(val)
		case "out_time":
			m.OutTime = parseFFTime(val)
		case "progress":
			m.Valid = true
			onUpdate(m)
		}
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

func atof(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// parseKbits turns "3212.3kbits/s" into 3212.3. "N/A" → 0.
func parseKbits(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "kbits/s")
	return atof(s)
}

// parseSpeed turns "1.02x" into 1.02. "N/A" → 0.
func parseSpeed(s string) float64 {
	return atof(strings.TrimSuffix(strings.TrimSpace(s), "x"))
}

// parseFFTime parses ffmpeg's "HH:MM:SS.microseconds" out_time.
func parseFFTime(s string) time.Duration {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h := atof(parts[0])
	m := atof(parts[1])
	sec := atof(parts[2])
	return time.Duration((h*3600+m*60+sec)*float64(time.Second)) * 1
}
