package verify

import (
	"strconv"
	"strings"
)

// Segment is one media segment parsed from an HLS media playlist.
type Segment struct {
	URI           string
	Duration      float64 // EXTINF seconds
	Discontinuity bool    // preceded by EXT-X-DISCONTINUITY
	DateRange     string  // raw EXT-X-DATERANGE line, if any
	CueOut        bool    // EXT-X-CUE-OUT present
	CueIn         bool    // EXT-X-CUE-IN present
}

// MediaPlaylist is a parsed HLS media playlist (the segment-level manifest).
type MediaPlaylist struct {
	TargetDuration int
	Segments       []Segment
	DateRanges     []string // all EXT-X-DATERANGE lines in order
}

// ParseMediaPlaylist parses an HLS media playlist. It is intentionally minimal —
// enough to run the verification checks (EXTINF durations, discontinuity tags,
// SCTE marker tags). It is not a general HLS library.
func ParseMediaPlaylist(text string) *MediaPlaylist {
	mp := &MediaPlaylist{}
	var pending Segment
	haveExtinf := false

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			mp.TargetDuration, _ = strconv.Atoi(after(line, ":"))
		case strings.HasPrefix(line, "#EXT-X-DISCONTINUITY"):
			pending.Discontinuity = true
		case strings.HasPrefix(line, "#EXTINF:"):
			pending.Duration = parseExtinf(after(line, ":"))
			haveExtinf = true
		case strings.HasPrefix(line, "#EXT-X-DATERANGE:"):
			pending.DateRange = line
			mp.DateRanges = append(mp.DateRanges, line)
		case strings.HasPrefix(line, "#EXT-X-CUE-OUT"):
			pending.CueOut = true
		case strings.HasPrefix(line, "#EXT-X-CUE-IN"):
			pending.CueIn = true
		case strings.HasPrefix(line, "#"):
			// other tags ignored
		default:
			// A URI line closes the current segment.
			if haveExtinf {
				pending.URI = line
				mp.Segments = append(mp.Segments, pending)
				pending = Segment{}
				haveExtinf = false
			}
		}
	}
	return mp
}

// IsMaster reports whether text is a master (variant) playlist rather than media.
func IsMaster(text string) bool {
	return strings.Contains(text, "#EXT-X-STREAM-INF")
}

// FirstVariantURI returns the first variant URI from a master playlist.
func FirstVariantURI(text string) string {
	lines := strings.Split(text, "\n")
	for i, raw := range lines {
		if strings.HasPrefix(strings.TrimSpace(raw), "#EXT-X-STREAM-INF") {
			for _, next := range lines[i+1:] {
				n := strings.TrimSpace(next)
				if n != "" && !strings.HasPrefix(n, "#") {
					return n
				}
			}
		}
	}
	return ""
}

// DiscontinuityCount counts EXT-X-DISCONTINUITY markers.
func (mp *MediaPlaylist) DiscontinuityCount() int {
	n := 0
	for _, s := range mp.Segments {
		if s.Discontinuity {
			n++
		}
	}
	return n
}

func parseExtinf(s string) float64 {
	// "4.000,title" → 4.000
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func after(line, sep string) string {
	if i := strings.Index(line, sep); i >= 0 {
		return strings.TrimSpace(line[i+len(sep):])
	}
	return ""
}
