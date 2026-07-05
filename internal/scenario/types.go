// Package scenario defines the declarative YAML scenario model that drives a
// streamwreck run: a source/encoder/output definition plus a timeline of
// timestamped impairment events. See streamwreck-spec.md §5.
package scenario

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration so YAML values like "40ms", "30s", "2s" parse
// via time.ParseDuration. A bare number is rejected — scenarios must be explicit.
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }
func (d Duration) String() string     { return time.Duration(d).String() }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"40ms\" or \"30s\": %w", err)
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Bitrate is a bits-per-second value parsed from forms like "3M", "800kbit",
// "128k". It renders back to ffmpeg/tc-friendly strings on demand.
type Bitrate int64 // bits per second

func (b Bitrate) BitsPerSecond() int64 { return int64(b) }

// FFmpeg renders the rate the way ffmpeg's -b:v/-maxrate/-bufsize expect: a
// plain bit count (ffmpeg accepts K/M suffixes but a raw integer is unambiguous).
func (b Bitrate) FFmpeg() string { return strconv.FormatInt(int64(b), 10) }

// TC renders the rate the way tc expects: "<n>kbit". tc's kbit is 1000 bits/s.
func (b Bitrate) TC() string {
	return strconv.FormatInt(int64(b)/1000, 10) + "kbit"
}

func (b *Bitrate) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("bitrate must be a string like \"3M\" or \"800kbit\": %w", err)
	}
	parsed, err := ParseBitrate(s)
	if err != nil {
		return err
	}
	*b = parsed
	return nil
}

// ParseBitrate accepts an optional decimal number with a unit suffix. Recognized
// suffixes (case-insensitive): "" or "bit" (bits), "k"/"kbit" (1e3), "m"/"mbit"
// (1e6), "g"/"gbit" (1e9). A trailing "bit"/"bps" is tolerated after the SI letter.
func ParseBitrate(s string) (Bitrate, error) {
	raw := strings.TrimSpace(strings.ToLower(s))
	if raw == "" {
		return 0, fmt.Errorf("empty bitrate")
	}
	raw = strings.TrimSuffix(raw, "bps")
	raw = strings.TrimSuffix(raw, "bit")
	mult := float64(1)
	switch {
	case strings.HasSuffix(raw, "g"):
		mult, raw = 1e9, strings.TrimSuffix(raw, "g")
	case strings.HasSuffix(raw, "m"):
		mult, raw = 1e6, strings.TrimSuffix(raw, "m")
	case strings.HasSuffix(raw, "k"):
		mult, raw = 1e3, strings.TrimSuffix(raw, "k")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid bitrate %q: %w", s, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("bitrate %q must not be negative", s)
	}
	return Bitrate(n * mult), nil
}

// Percent is a percentage parsed from forms like "0.5%", "15%", or a bare "3".
type Percent float64

func (p Percent) Value() float64 { return float64(p) }

// TC renders the percent the way tc/netem expects: a number followed by "%".
func (p Percent) TC() string {
	return strconv.FormatFloat(float64(p), 'f', -1, 64) + "%"
}

func (p *Percent) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		// Allow a bare numeric node too.
		var f float64
		if ferr := value.Decode(&f); ferr != nil {
			return fmt.Errorf("percent must be like \"15%%\": %w", err)
		}
		*p = Percent(f)
		return nil
	}
	parsed, err := ParsePercent(s)
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

func ParsePercent(s string) (Percent, error) {
	raw := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid percent %q: %w", s, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("percent %q must not be negative", s)
	}
	return Percent(n), nil
}
