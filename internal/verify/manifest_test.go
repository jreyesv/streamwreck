package verify

import "testing"

const sampleMedia = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:2.000,
seg0.ts
#EXTINF:2.000,
seg1.ts
#EXT-X-DISCONTINUITY
#EXTINF:2.000,
seg2.ts
#EXT-X-DATERANGE:ID="1",SCTE35-OUT=0xFC30
#EXT-X-CUE-OUT:30.000
#EXTINF:2.000,
seg3.ts
`

func TestParseMediaPlaylist(t *testing.T) {
	mp := ParseMediaPlaylist(sampleMedia)
	if mp.TargetDuration != 2 {
		t.Errorf("target duration = %d, want 2", mp.TargetDuration)
	}
	if len(mp.Segments) != 4 {
		t.Fatalf("segments = %d, want 4", len(mp.Segments))
	}
	if mp.DiscontinuityCount() != 1 {
		t.Errorf("discontinuities = %d, want 1", mp.DiscontinuityCount())
	}
	if !mp.Segments[2].Discontinuity {
		t.Error("segment 2 should carry the discontinuity flag")
	}
	if !mp.Segments[3].CueOut {
		t.Error("segment 3 should carry CUE-OUT")
	}
	if len(mp.DateRanges) != 1 {
		t.Errorf("date ranges = %d, want 1", len(mp.DateRanges))
	}
}

func TestMasterPlaylistDetection(t *testing.T) {
	master := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=3000000
stream.m3u8
`
	if !IsMaster(master) {
		t.Error("should detect master playlist")
	}
	if got := FirstVariantURI(master); got != "stream.m3u8" {
		t.Errorf("first variant = %q, want stream.m3u8", got)
	}
	if IsMaster(sampleMedia) {
		t.Error("media playlist misidentified as master")
	}
}

func TestExtractMarkers(t *testing.T) {
	mp := ParseMediaPlaylist(sampleMedia)
	markers := extractMarkers(mp)
	if len(markers) != 1 {
		t.Errorf("expected 1 marker from CUE-OUT, got %d", len(markers))
	}
}
