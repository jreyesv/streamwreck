# streamwreck

A CLI tool that spins up livestreams with ffmpeg and subjects them to **reproducible** adverse
conditions — unstable connections, bandwidth collapse, encoder reconnects, PTS jumps, and
misaligned SCTE-35 ad-splice markers — then verifies what came out against what was signaled going
in. For testing streaming and SSAI pipelines against realistic failure modes on demand.

Every run is driven by a declarative YAML scenario, so failures are reproducible and diffable, and
`run` exits non-zero when a verification check fails — making it usable as a CI gate.

See [streamwreck-spec.md](streamwreck-spec.md) for the full design.

## Quick start

**Requirements:** [Go](https://go.dev/dl/) 1.26+ and [Docker](https://docs.docker.com/get-docker/)
(Desktop on macOS/Windows, or Engine + Compose v2 on Linux). streamwreck isn't a standalone binary —
it drives a Docker Compose lab that lives in this repo, so you **clone and build**, you don't just
`go install`.

```bash
# 1. get it
git clone https://github.com/<owner>/streamwreck.git
cd streamwreck

# 2. build the CLI (produces ./streamwreck)
go build -o streamwreck ./cmd/streamwreck

# 3. bring the lab up (first run builds the encoder/shaper/MediaMTX images — a few minutes)
./streamwreck up

# 4. run the baseline to confirm everything works end to end
./streamwreck run --preset clean         # → HLS stream, verified, exits 0 on PASS

# 5. explore
./streamwreck presets                    # list the 11 bundled scenarios
./streamwreck run --preset flaky-wifi     # degrade the uplink and watch it recover
./streamwreck init                        # scaffold a scenario for your own platform

# 6. tear down when done
./streamwreck down
```

Optionally put it on your PATH (`sudo mv streamwreck /usr/local/bin/`), but keep running `up`/`down`
from the repo root so it finds `deploy/docker-compose.yml` (or pass `--compose /path/to/it`).

## How it works

The `streamwreck` binary runs on the **host** and orchestrates a Docker Compose lab via
`docker`/`docker compose exec`:

```
host: streamwreck run scenario.yaml
  ├─ docker compose up (ensure the lab is online)
  ├─ exec encoder       ffmpeg <argv built from the encoder config>   (supervised)
  ├─ exec shaper        tc / netem / htb  on eth0   (encoder egress, shared netns)
  ├─ exec shaper        threefive         (author + decode SCTE-35 cues)
  ├─ exec player-shaper ip / tc / ifb0    (downstream pull impairment)
  └─ exec player        ffmpeg pull + curl manifest   (verify)
```

**Why a host orchestrator + sidecars?** (spec §4.2 — these are load-bearing)

- The **encoder** and **player** images stay clean and unprivileged. Only the **shaper** sidecars
  carry `NET_ADMIN` + `tc`. A shaper joins its target's network namespace
  (`network_mode: "service:encoder"`), so `tc … dev eth0` inside the shaper impairs the target's
  interface while the target image has no shaping capability at all.
- All shaping happens inside a container netns, so it can never touch the host or other containers.
- netem is **egress-only**. The download path (player pull) is shaped by redirecting ingress to an
  **IFB** device and attaching netem to its egress.
- Accurate bandwidth stacks netem **under** an `htb` class — netem `rate` alone is only an
  approximation.
- Bitrate spikes are **content-driven** (`complex` source) or an encoder **restart**, never a
  commanded timeline dial (they correspond to nothing real).

## Layout

```
cmd/streamwreck/     cobra entrypoint + subcommands
internal/
  scenario/          YAML load, custom types (Duration/Bitrate/Percent/NetworkSpec), validate
  run/               Runner interface (docker exec / compose) + fake for tests
  encoder/           ffmpeg argv builder (pure) + process supervisor
  shaper/            tc/netem/htb/ifb command builders (pure) + executor
  scte/              SCTE-35 schedule (pure) + threefive author/decode wrappers
  verify/            HLS manifest parser + checks + player-based join/rebuffer measurement
  report/            JSON report model + human printer
  controller/        orchestrates a run: timeline stepper, chaos actions, teardown
deploy/              docker-compose.yml, encoder/shaper Dockerfiles, mediamtx.yml
presets/             11 bundled scenarios (embedded into the binary)
```

## Usage

```bash
streamwreck init                     # guided setup: scaffold a scenario for YOUR ingest
streamwreck up                       # bring the lab online (builds images first run)
streamwreck presets                  # list bundled presets
streamwreck validate <scenario.yaml> # lint a scenario, no execution
streamwreck run <scenario.yaml>      # execute end to end (exits non-zero on check failure)
streamwreck run --preset flaky-wifi  # run a bundled preset by name
streamwreck run --preset flaky-wifi --source-file myclip.mp4  # stream your own video
streamwreck report <report.json>     # pretty-print a verification report
streamwreck down                     # tear the lab down
```

A run holds the stream for the length of the timeline (min ~90s) so the origin produces enough
manifest/segments for verification, then pulls the HLS manifest and runs the enabled checks.

### Testing your own platform

`streamwreck init` walks you through pointing a scenario at a real streaming platform — it asks for
your **ingest URL + stream key** (`output.url`, where the encoder publishes) and, optionally, a
**playback URL** (`verify.pull`, where the verifier grades viewer QoE), then writes a ready-to-run
scenario with a starter impairment profile:

```bash
streamwreck init                     # interactive (asks for a name, ingest, playback, profile)
# or fully scripted:
streamwreck init --name mytest \
  --ingest "rtmp://ingest.yourplatform.com/app/STREAMKEY" \
  --pull   "https://play.yourplatform.com/.../index.m3u8" \
  --profile flaky-uplink
streamwreck run --preset mytest      # resolvable by name
```

Generated scenarios are written to `presets/user/<name>.yaml` (gitignored, so your stream keys
never get committed) and are resolvable by name via `--preset`, alongside the bundled ones —
`streamwreck presets` lists both. Pass `-o <path>` to write elsewhere.

Profiles: `clean` (connectivity + baseline QoE), `flaky-uplink` (delay/jitter/loss burst + bandwidth
cap, then recovery), `reconnect` (encoder restart + kill/respawn — a broadcaster reconnecting). Point
it at a **staging/test channel**: each run creates a real live stream (recordings, notifications,
ads). When `output.url` targets an external ingest, the bundled MediaMTX `ingest` container simply
goes unused; the encoder's egress shaping still degrades the real uplink, all inside the container's
netns.

Two guardrails make misconfiguration self-explanatory:

- **Amazon IVS URLs are auto-repaired.** An IVS ingest (`*.contribute.live-video.net`) requires
  `rtmps://<host>:443/app/<stream-key>`; a stream key pasted straight onto the host is silently
  rejected during the TLS handshake. `init`, `validate`, and `run` detect this and add the `:443`
  port and `/app/` path (and upgrade `rtmp`→`rtmps`), logging what changed.
- **Fail-fast on a dead encoder.** If ffmpeg exits within the first ~2s (bad URL, rejected stream
  key, wrong protocol), the run aborts immediately and prints ffmpeg's own log tail — instead of
  holding the full run and then failing verification against a stream that never existed. The
  encoder's ffmpeg output also streams live during a run.

### Choosing the video source

The `source:` block picks what gets encoded: `testsrc2` (test pattern, default), `smpte` (color
bars), `complex` (high-entropy motion that forces VBR bitrate to climb), or `file` (your own video).

To stream your own footage, drop it in the repo's `media/` folder (mounted read-only into the
encoder at `/media`) and either set it in the scenario:

```yaml
source: { type: file, file: /media/myclip.mp4, timecode_overlay: true }
```

…or override any scenario at run time without editing YAML:

```bash
streamwreck run --preset flaky-wifi --source-file myclip.mp4
```

`--source-file` resolves a bare/relative name under `/media` (an absolute path is used as-is),
switches the source to `file`, and keeps the scenario's resolution/fps/timecode settings. File
sources loop forever (`-stream_loop -1`), so a short clip fills a full run.

## Validation status

Validated **live** against the Docker stack on Docker Desktop for Mac (ffmpeg image runs amd64 under
emulation — functional, just slower than native):

| Area | Status |
|---|---|
| Phase 0 — tc in the shaper's shared netns; host never shaped | ✅ `noqueue` → netem visible on `eth0` |
| Phase 1 — clean run → playable HLS, 2s keyframe cadence, burned-in PTS | ✅ `TARGETDURATION:2`, all `EXTINF:2.0` |
| Phase 2 — netem egress changes at scheduled offsets | ✅ delay/jitter/loss appear then clear |
| Phase 3 — `accurate: true` stacks an `htb` root under netem | ✅ `htb 1: root` installed on rate events |
| Phase 6 — verify pulls manifest, runs checks, writes report, non-zero exit | ✅ `clean` PASS, report written |
| SCTE-35 — threefive authoring/decoding of cues | ✅ valid base64 cues authored per schedule live |
| Unit tests — parsing, tc argv, encoder argv, schedule, manifest, report | ✅ `go test ./...` green |

**Environment caveats (both flagged in the plan as prototype/host-dependent):**

- **Downstream (`degrade_player`) shaping needs the `ifb` kernel module.** It ships with mainstream
  Linux and CI runners but is **absent from Docker Desktop for Mac's LinuxKit kernel**, so the IFB
  path returns an actionable error there and the run continues with the origin healthy. The ingress
  qdisc + redirect filter syntax is correct and works on a Linux host (or with `modprobe ifb` on the
  Docker host).
- **SCTE-35 live *injection* into the manifest is the spec §12 open decision.** Cue **authoring** and
  **decoding** are validated live; landing the authored cues in the HLS manifest as
  `EXT-X-DATERANGE`/`CUE-OUT` depends on the mpegts injection path and is the item to prototype on a
  real Linux host. The verifier reports markers as missing when they don't land — which is the
  honest result a test tool should give, not a false pass.

## Development

```bash
go test ./...        # unit tests (no Docker required)
go build ./...       # build all packages
go build -o streamwreck ./cmd/streamwreck
```

## License

[MIT](LICENSE) © 2026 Juan Reyes
