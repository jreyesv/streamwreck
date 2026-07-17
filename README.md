# streamwreck

Test your live streaming platform against the failure modes it can't control but gets blamed for —
broadcasters on flaky connections, bandwidth collapse, encoder reconnects, PTS jumps — **on demand
and reproducibly**, instead of waiting to hit them in the wild.

streamwreck acts as a broadcaster with a deliberately bad connection: it publishes a real stream to
**your** ingest while shaping the uplink (delay, jitter, loss, bandwidth caps, reconnects), then
pulls your playback URL back and grades what viewers actually experience — join time, rebuffering,
segment/discontinuity correctness. Every run is a declarative YAML scenario, so a failure becomes a
repeatable experiment you can debug against and gate deploys on. `run` exits non-zero when a check
fails, so it drops straight into CI.

> New here and just want to see it work? Jump to [Try it without a platform](#try-it-without-a-platform).

## Requirements

[Go](https://go.dev/dl/) 1.26+ and [Docker](https://docs.docker.com/get-docker/) (Desktop on
macOS/Windows, or Engine + Compose v2 on Linux). streamwreck drives a small Docker stack that lives
in this repo, so **clone and build** — it isn't a `go install` binary.

```bash
git clone https://github.com/<owner>/streamwreck.git
cd streamwreck
go build -o streamwreck ./cmd/streamwreck    # produces ./streamwreck
```

## Test your platform

```bash
# 1. scaffold a scenario pointed at your ingest (interactive: name, ingest URL, playback URL, profile)
./streamwreck init

# 2. start the testing machinery (encoder + shapers + player)
./streamwreck up

# 3. run it — publishes to your ingest, degrades the uplink, grades viewer QoE, writes a report
./streamwreck run --preset <your-name>

# 4. tear down
./streamwreck down
```

`init` asks for:

- **Ingest URL** (`output.url`) — where the encoder publishes, **including your stream key**, e.g.
  `rtmp://ingest.yourplatform.com/app/STREAMKEY`.
- **Playback URL** (`verify.pull`, optional) — where the verifier pulls to grade viewer QoE. Skip it
  to test the uplink only.
- **Profile** — a starter impairment timeline: `clean` (connectivity + baseline QoE),
  `flaky-uplink` (delay/jitter/loss burst + bandwidth cap, then recovery), or `reconnect` (encoder
  restart + kill/respawn — a broadcaster reconnecting).

Scenarios are written to `presets/user/<name>.yaml` (**gitignored, so stream keys never get
committed**) and are runnable by name via `--preset`. Fully scripted form:

```bash
./streamwreck init --name mytest \
  --ingest "rtmp://ingest.yourplatform.com/app/STREAMKEY" \
  --pull   "https://play.yourplatform.com/.../index.m3u8" \
  --profile flaky-uplink
./streamwreck run --preset mytest
```

> **Use a staging/test channel.** Each run creates a real live stream on your platform — recordings,
> follower notifications, and ad impressions are real side effects. Only test infrastructure you own
> or are authorized to.

### Live dashboard

While a scenario runs, streamwreck shows a live, in-place dashboard — static stream facts
(resolution, framerate, keyframe interval, target bitrate) alongside live metrics parsed from
ffmpeg, plus the current network impairment:

```
  ⠋ streamwreck  flaky-wifi      streaming 00:34 / 01:30

  stream    1280x720 · 30fps · keyframe 2.0s · target 3.0 Mbps · rtmp
  bitrate   2680 kbps    ████████████████░░   89%
  rate      24.8fps · speed 0.83× · 744 frames · 12 dropped
  network   delay 200ms±80ms · loss 12% · rate 800kbit (htb)
```

The bitrate bar and `speed` turn red when the encoder falls behind realtime — starvation shows up
instantly. It animates in a terminal and falls back to periodic plain lines when output isn't a TTY
(CI/logs); `NO_COLOR` disables color.

### What it's actually testing

Not the network conditioning itself — that's the *stimulus*. It measures **how your pipeline reacts
to a degraded broadcaster**: does ingest hold the connection through loss, or drop it? Does the
transcoder stay clean under bursty frames? Does a reconnect produce a proper discontinuity or a black
screen for viewers? And the money question — when the *broadcaster* has 15% loss, what do *viewers*
see? The value is turning broadcaster-side failures (which you can't control and usually can't
reproduce) into a deterministic experiment you can debug against and regression-gate in CI.

## Writing scenarios

A scenario is YAML with a source/encoder/output and a timeline of timestamped impairments. `init`
generates one for you; edit it or write your own. Key blocks:

**Source** — what gets encoded:

```yaml
source:
  type: testsrc2          # testsrc2 (pattern) | smpte (bars) | complex (high-motion VBR) | file
  resolution: 1280x720
  fps: 30
  timecode_overlay: true  # burn PTS into the frame for latency/drift measurement
```

To stream your own footage, drop it in `media/` (mounted read-only into the encoder at `/media`) and
use `type: file, file: /media/clip.mp4` — or override any scenario without editing it:

```bash
./streamwreck run --preset mytest --source-file gameplay.mp4
```

**Timeline** — impairments fire at their offset from stream start:

```yaml
timeline:
  - at: 30s
    network: { delay: 200ms, jitter: 80ms, loss: 10%, rate: 2500kbit, accurate: true }
  - at: 90s
    network: clear
  - at: 120s
    action: restart_encoder     # also: kill_encoder, av_desync, pts_jump, keyframe_misalign
```

`accurate: true` enforces the bandwidth cap with an `htb` shaper (netem `rate` alone is only
approximate). **Verify** pulls the manifest and runs checks (`join_time`, `rebuffering`,
`segment_duration`, `discontinuity_tags`, `scte_markers`) into a JSON report.

**Run length.** By default the stream runs for `max(last event offset, 60s) + 30s`, then holds
through verification. Set an explicit length with a top-level `duration:` (e.g. `duration: 10m` for a
soak test) or override any scenario at run time with `--duration`:

```bash
./streamwreck run --preset mytest --duration 10m
```

Two guardrails make misconfiguration self-explanatory:

- **Amazon IVS URLs are auto-repaired.** An IVS ingest (`*.contribute.live-video.net`) needs
  `rtmps://<host>:443/app/<key>`; a key pasted straight onto the host is rejected during the TLS
  handshake. `init`/`validate`/`run` add the `:443` port and `/app/` path (and upgrade `rtmp`→`rtmps`).
- **Fail-fast on a dead encoder.** If ffmpeg exits within ~2s (bad URL, rejected key, wrong protocol),
  the run aborts immediately and prints ffmpeg's log tail instead of failing verification later.

## Try it without a platform

Don't have an ingest handy? A bundled **MediaMTX** origin gives you a self-contained demo. Start it
with `--lab` and run any of the 11 bundled example scenarios against it:

```bash
./streamwreck up --lab                 # also starts the demo origin (MediaMTX)
./streamwreck run --preset clean       # baseline → playable HLS, verified, exits 0
./streamwreck run --preset flaky-wifi  # degrade the uplink and watch it recover
./streamwreck presets                  # list bundled + your own scenarios
```

Watch the demo stream at `http://localhost:8888/live/stream/index.m3u8` (VLC/Safari). Without
`--lab`, `up` starts only the testing machinery and the demo origin stays out of the way.

## Commands

```
streamwreck init                       # scaffold a scenario for your ingest
streamwreck up [--lab]                  # start the machinery (--lab adds the demo origin)
streamwreck run <scenario|--preset X>   # execute end to end; non-zero exit on check failure
streamwreck validate <scenario|--preset X>
streamwreck presets                     # list bundled + user scenarios
streamwreck report <report.json>        # pretty-print a report
streamwreck down                        # tear everything down
```

Run from the repo root so it finds `deploy/docker-compose.yml`, or pass `--compose <path>`.

## How it works

The `streamwreck` binary runs on the **host** and drives containers over `docker compose exec`:

```
streamwreck run
  ├─ exec encoder       ffmpeg <argv from the scenario>     → publishes to output.url
  ├─ exec shaper        tc / netem / htb  on eth0           (encoder egress, shared netns)
  └─ exec player        ffmpeg pull + curl manifest         (grade viewer QoE)
```

The design constraint (spec §4.2): the **encoder/player images stay clean and unprivileged**; only
the **shaper sidecars** carry `NET_ADMIN` + `tc`, joining their target's network namespace
(`network_mode: "service:encoder"`) so `tc … dev eth0` shapes the target's interface. All shaping
happens inside a container netns — it can never touch the host. See
[streamwreck-spec.md](streamwreck-spec.md) for the full design and
[deploy/](deploy/) for the stack.

## Known constraints

- **Viewer-side shaping (`degrade_player`) needs the `ifb` kernel module** — present on mainstream
  Linux and CI runners, **absent from Docker Desktop for Mac's kernel**, where that path returns an
  actionable error and the run continues with a healthy origin.
- **SCTE-35 live *injection* is a work in progress.** Cue authoring/decoding (threefive) is validated;
  landing authored cues in the manifest as `EXT-X-DATERANGE`/`CUE-OUT` is host/format dependent, so
  the verifier reports markers as missing when they don't land rather than falsely passing.

## Development

```bash
go test ./...        # unit tests (no Docker required)
go build ./...
```

## License

[MIT](LICENSE) © 2026 Juan Reyes
