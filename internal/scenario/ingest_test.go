package scenario

import "testing"

func TestNormalizeIngestURL_IVS(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{
			name:    "missing port and app path (the reported bug)",
			in:      "rtmps://fa723fc1b171.global-contribute.live-video.net/sk_us-west-2_KEY",
			want:    "rtmps://fa723fc1b171.global-contribute.live-video.net:443/app/sk_us-west-2_KEY",
			changed: true,
		},
		{
			name:    "rtmp upgraded to rtmps",
			in:      "rtmp://x.global-contribute.live-video.net/sk_KEY",
			want:    "rtmps://x.global-contribute.live-video.net:443/app/sk_KEY",
			changed: true,
		},
		{
			name:    "already correct is untouched",
			in:      "rtmps://x.global-contribute.live-video.net:443/app/sk_KEY",
			want:    "rtmps://x.global-contribute.live-video.net:443/app/sk_KEY",
			changed: false,
		},
		{
			name:    "regional contribute endpoint also matched",
			in:      "rtmps://x.us-west-2.contribute.live-video.net/sk_KEY",
			want:    "rtmps://x.us-west-2.contribute.live-video.net:443/app/sk_KEY",
			changed: true,
		},
		{
			name:    "non-IVS ingest untouched",
			in:      "rtmp://ingest.twitch.tv/app/live_KEY",
			want:    "rtmp://ingest.twitch.tv/app/live_KEY",
			changed: false,
		},
		{
			name:    "arbitrary rtmp untouched",
			in:      "rtmp://my.origin.example/live/stream",
			want:    "rtmp://my.origin.example/live/stream",
			changed: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, note := NormalizeIngestURL(c.in)
			if got != c.want {
				t.Errorf("url = %q, want %q", got, c.want)
			}
			if (note != "") != c.changed {
				t.Errorf("changed = %v (note %q), want %v", note != "", note, c.changed)
			}
		})
	}
}
