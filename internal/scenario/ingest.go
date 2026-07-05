package scenario

import (
	"net/url"
	"strings"
)

// ivsIngestSuffix identifies an Amazon IVS RTMPS ingest host. Both the global
// (<id>.global-contribute.live-video.net) and regional
// (<id>.<region>.contribute.live-video.net) forms end in this suffix. IVS
// requires the ingest URL to be rtmps://<host>:443/app/<stream-key>; a stream
// key pasted straight onto the host — the common mistake — is silently rejected
// during the TLS handshake.
const ivsIngestSuffix = "contribute.live-video.net"

// NormalizeIngestURL repairs well-known ingest-URL mistakes and returns the
// corrected URL plus a human note describing what changed (empty when nothing
// changed or the URL is not a recognized ingest). Currently handles Amazon IVS:
// upgrades rtmp→rtmps, adds the :443 port, and inserts the required /app/ path.
func NormalizeIngestURL(raw string) (normalized, note string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw, ""
	}
	if !strings.HasSuffix(strings.ToLower(u.Hostname()), ivsIngestSuffix) {
		return raw, "" // not an IVS ingest — leave untouched
	}

	var notes []string
	if u.Scheme == "rtmp" {
		u.Scheme = "rtmps"
		notes = append(notes, "upgraded to rtmps (IVS is TLS-only)")
	}
	if u.Port() == "" {
		u.Host = u.Hostname() + ":443"
		notes = append(notes, "added :443")
	}
	if key := strings.TrimPrefix(u.Path, "/"); key != "" && !strings.HasPrefix(u.Path, "/app/") && u.Path != "/app" {
		u.Path = "/app/" + key
		notes = append(notes, "inserted /app/ path")
	}

	if len(notes) == 0 {
		return raw, ""
	}
	return u.String(), "Amazon IVS ingest: " + strings.Join(notes, ", ")
}
