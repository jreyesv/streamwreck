# streamwreck — Technical Specification

> A CLI tool that spins up livestreams with ffmpeg and subjects them to reproducible adverse conditions — unstable connections, bandwidth collapse, bitrate spikes, encoder reconnects, and misaligned ad-splice markers — for testing streaming and SSAI pipelines.
>
> **Name is a placeholder** — swap `streamwreck` throughout if you settle on something else.

---

## 1. Purpose

Build a deterministic test harness that generates a livestream and applies scheduled impairments to it, so streaming/SSAI pipelines can be tested against realistic failure modes without waiting for them to occur naturally. Every run is driven by a declarative scenario file, so failures are reproducible and diffable.

Two impairment layers compose independently:

- **Encoder layer** (ffmpeg) — controls what the stream *is*: bitrate behavior, GOP structure, keyframe cadence, timestamps, A/V sync, deliberate discontinuities and reconnects, and SCTE-35 marker placement.
- **Network layer** (tc/netem) — controls what happens to the bytes in flight: latency, jitter, loss, bandwidth starvation, and time-varying versions of each.

A Go controller reads a scenario timeline, launches the encoder, and mutates network rules on a schedule, then verifies what came out the other end against what was signaled going in.

## 2. Non-goals

- Not a load/scale tester (not simulating thousands of concurrent viewers).
- Not a real ad server or SSAI stitcher — it *exercises* one, it doesn't replace one.
- Not a GUI. CLI + config files only. (A future TUI/status view is out of scope for v1.)
- Not intended to run against production ingest. Ships with a local ingest container.

## 3. Tech stack

| Concern | Choice | Rationale |
|---|---|---|
| Controller/CLI | **Go** | Good process supervision, clean `exec` of `ffmpeg`/`tc`, static binary, matches the author's toolchain. |
| CLI framework | `cobra` | Standard, subcommand ergonomics. |
| Config | YAML via `gopkg.in/yaml.v3` | Human-editable scenario timelines. |
| Encoder | **ffmpeg** (libx264, aac) | Source generation + encode + mux. |
| Network shaping | **iproute2** (`tc`, `netem`, `htb`/`tbf`, `ifb`) | Kernel-level, per-namespace impairment. |
| SCTE-35 authoring/decode | **threefive** (Python CLI, shelled out) | ffmpeg passes SCTE-35 through but does not author it. threefive builds `splice_insert`/`time_signal` and decodes markers for verification. |
| Local ingest/origin | **MediaMTX** | Accepts RTMP+SRT in, republishes HLS/DASH out — one container covers ingest and playback origin. |
| Orchestration | **Docker Compose** | Network-namespace isolation is the whole point (see §4). |

## 4. Architecture

### 4.1 Container topology

```
┌──────────────────────────────────────────────────────────┐
│ compose network: lab                                       │
│                                                            │
│  ┌─────────────┐   shares netns   ┌──────────────────┐    │
│  │  encoder    │◄─────────────────│  shaper          │    │
│  │  (ffmpeg)   │  network_mode:   │  (tc + ifb +     │    │
│  │  NO caps    │  service:encoder │   Go controller  │    │
│  │             │                  │   + threefive)   │    │
│  └──────┬──────┘                  │  cap_add:        │    │
│         │ RTMP/SRT uplink         │    NET_ADMIN     │    │
│         ▼                         └──────────────────┘    │
│  ┌─────────────┐                                           │
│  │  ingest     │  MediaMTX: RTMP/SRT in → HLS/DASH out     │
│  │  (origin)   │                                           │
│  └──────┬──────┘                                           │
│         │ HLS/DASH pull                                     │
│         ▼                                                   │
│  ┌─────────────┐   shares netns   ┌──────────────────┐    │
│  │  player     │◄─────────────────│  player-shaper   │    │
│  │  (ffmpeg    │                  │  (tc + ifb)      │    │
│  │   puller)   │                  │  NET_ADMIN       │    │
│  └─────────────┘                  └──────────────────┘    │
└──────────────────────────────────────────────────────────┘
```

### 4.2 Why this shape (critical design constraints)

These are the load-bearing decisions. Do not "simplify" them away.

1. **Isolate `NET_ADMIN` in a sidecar.** The encoder and player images stay clean and unprivileged. Only the shaper containers get `NET_ADMIN` and carry `tc` tooling. The shaper shares its target's network namespace via `network_mode: "service:encoder"`, so `tc qdisc ... dev eth0` inside the shaper impairs the encoder's interface while the encoder image itself has no special capability and no shaping tools baked in.

2. **Namespace isolation protects the host.** Because all shaping happens inside a container's netns on its veth, it can never affect the host's connectivity or other containers. Worst case is one wrecked container that restarts clean. This is the entire reason the tool is containerized — never shape a host interface.

3. **netem is egress-only.** For the uplink scenarios (encoder pushing RTMP/SRT out), the encoder's egress *is* the link under test — shape it directly. To impair a *download* path (player pulling manifest/segments), you must redirect ingress to an **IFB** device and attach netem to the IFB's egress. Forgetting this is the #1 cause of "my scenario has no effect." The player-shaper implements the IFB path.

4. **netem `rate` is an approximation.** It interacts with veth queueing and does not model realistic queue/bufferbloat behavior. When a scenario needs accurate bandwidth or queue dynamics, stack netem on top of an `htb` or `tbf` shaper rather than trusting netem `rate` alone. For pure loss/delay/jitter, netem alone is fine.

5. **Bitrate spikes are content-driven, not dial-driven — do not model them as a commanded event.** You cannot cleanly retarget libx264 bitrate mid-run, and that limitation reflects reality: real spikes are not commanded, they *emerge*. Two real phenomena produce them — (a) **content-complexity spikes**, where instantaneous VBR bitrate rises on hard-to-encode content (scene cuts, fast motion, crowds, water, confetti); and (b) **structural spikes**, where every I-frame is several times larger than surrounding P/B frames, so each GOP boundary is a periodic bump. Even "CBR" only holds constant averaged over the VBV window; instantaneously it still breathes, and `bufsize` is the knob controlling how much. What the pipeline actually experiences downstream is one of: a *burst of bytes* colliding with a constrained uplink (→ queue fill, loss, backpressure — overlaps with `congested-uplink`), *variable segment sizes* noising up the player's ABR estimation, or *VBV/buffer stress* from a tight `bufsize` against a high `maxrate`. Model those consequences directly (see the `complex-source` and `ad-boundary-mismatch` presets in §9). A genuine encoder bitrate change = an encoder **restart** (which doubles as a reconnect test). Never try to "spike the bitrate dial" on the timeline — it corresponds to nothing real.

## 5. Scenario schema

A scenario is a YAML file with a source/encoder/output definition and a **timeline** of timestamped events. The controller sorts events by `at`, launches the encoder at t=0, and fires each event when its offset elapses.

```yaml
name: flaky-wifi
description: Variable delay with periodic loss bursts on the encoder uplink.

source:
  type: testsrc2          # testsrc2 | smpte | file | complex
  file: null              # path when type=file (mounted into encoder)
  resolution: 1280x720
  fps: 30
  timecode_overlay: true  # burn PTS (HH:MM:SS.mmm) into video for latency/drift measurement
  # type=complex generates high-entropy motion (noise/mandelbrot/fast pans) that
  # forces VBR bitrate to climb — the honest way to produce content-driven spikes.
  complexity: high        # low | medium | high (only used when type=complex)

encoder:
  video_bitrate: 3M
  maxrate: 3M
  bufsize: 6M
  gop: 60                 # frames; at 30fps = 2s keyframe cadence
  keyint_min: 60          # force exact GOP (with sc_threshold 0)
  preset: veryfast
  tune: zerolatency
  audio_bitrate: 128k

output:
  protocol: rtmp          # rtmp | srt
  url: rtmp://ingest/live/stream

# Ordered by `at` (relative to stream start). Each entry has exactly one of:
# network | action | source_switch
timeline:
  - at: 0s
    network: { delay: 40ms, jitter: 10ms, loss: 0.5% }
  - at: 30s
    network: { delay: 200ms, jitter: 80ms, loss: 15%, rate: 800kbit }
  - at: 60s
    network: clear
  - at: 60s
    action: restart_encoder      # discontinuity + reconnect test
  - at: 90s
    action: av_desync            # applies itsoffset on next (re)start
    params: { offset: 250ms }
  - at: 120s
    action: pts_jump
    params: { jump: 5s }

# Optional. Requires threefive.
scte35:
  enabled: true
  type: time_signal            # time_signal | splice_insert
  cadence: 30s                 # marker every N seconds
  preroll: 4s                  # splice PTS is this far in the future
  break_duration: 30s
  misalign: false              # true = deliberately offset splice from GOP boundary

# Optional. Splices a real ad segment at each SCTE break so you can test the
# content<->ad boundary. Set a profile deliberately different from the program
# (bitrate/resolution/gop) to reproduce ad-boundary quality steps and stalls.
ad:
  enabled: false
  source: { type: complex, complexity: medium }   # or file: /ads/spot.mp4
  encoder:
    video_bitrate: 6M          # e.g. double the program's 3M -> profile mismatch
    resolution: 1920x1080      # different ladder rung than the program
    gop: 90                    # different keyframe cadence than the program
  duration: 30s

# Optional. Runs the player + verifier.
verify:
  enabled: true
  pull: http://ingest:8888/live/index.m3u8
  degrade_player: false        # if true, apply player-shaper impairment too
  checks:
    - segment_duration         # segments are multiples of GOP duration
    - discontinuity_tags       # EXT-X-DISCONTINUITY appears at reconnects
    - scte_markers             # manifest markers match authored SCTE-35
    - join_time                # time-to-first-frame on join
    - rebuffering              # stalls during playback
  report: ./reports/flaky-wifi.json
```

**Network block fields:** `delay`, `jitter` (delay variation), `loss` (%), `corrupt` (%), `duplicate` (%), `reorder` (%), `rate` (bandwidth cap). `network: clear` removes all qdiscs. When `rate` is present and `accurate: true`, stack htb/tbf under netem.

**Actions:** `restart_encoder`, `av_desync`, `pts_jump`, `kill_encoder` (leave dead N seconds), `keyframe_misalign` (shift GOP phase so splice points miss IDR frames).

## 6. CLI surface

```
streamwreck run <scenario.yaml>        # execute a scenario end to end
streamwreck presets                     # list bundled presets
streamwreck run --preset flaky-wifi     # run a bundled preset by name
streamwreck validate <scenario.yaml>    # lint/typecheck a scenario, no execution
streamwreck report <report.json>        # pretty-print a verification report
streamwreck up / down                   # bring the compose stack up/down manually
```

`run` exits non-zero if any enabled verification check fails — makes it usable in CI.

## 7. Module layout

```
streamwreck/
├── cmd/streamwreck/          # cobra entrypoint, subcommands
├── internal/
│   ├── scenario/             # YAML load, validate, timeline sort
│   ├── controller/           # orchestrates a run: timeline stepper, teardown
│   ├── encoder/              # builds ffmpeg argv from encoder config; supervises process
│   ├── shaper/               # execs tc/netem/htb/ifb; apply/clear/change
│   ├── scte/                 # threefive wrapper: author markers, decode manifest markers
│   ├── verify/               # pull manifest, run checks, emit report
│   └── report/               # JSON report model + printer
├── deploy/
│   ├── docker-compose.yml
│   ├── encoder/Dockerfile    # ffmpeg only, no caps
│   ├── shaper/Dockerfile     # iproute2 + threefive + streamwreck binary
│   └── mediamtx.yml
├── presets/                  # bundled scenario YAMLs (see §9)
└── reports/                  # verification output
```

## 8. Build phases

Build incrementally. Each phase has an acceptance criterion that must pass before moving on.

**Phase 0 — Scaffolding.** Compose stack with encoder, shaper (sharing encoder netns), and MediaMTX ingest. Encoder image has no caps; shaper has `NET_ADMIN` and `iproute2`.
*Accept:* `streamwreck up` brings the stack online; from inside the shaper, `tc qdisc show dev eth0` succeeds and `tc qdisc show` on the host is unchanged.

**Phase 1 — Encoder + baseline run.** `scenario` loader + `encoder` module. Launch ffmpeg from config with fixed GOP, timecode overlay, RTMP output to MediaMTX. No impairment yet.
*Accept:* `streamwreck run presets/clean.yaml` produces a playable HLS stream at the MediaMTX HLS URL with burned-in PTS timecode and 2s keyframe cadence.

**Phase 2 — Egress shaping + timeline.** `shaper` module (netem apply/change/clear on encoder egress) + `controller` timeline stepper.
*Accept:* `flaky-wifi` runs; `tc` rules visibly change at the scheduled offsets; the stream degrades and recovers accordingly.

**Phase 3 — Accurate bandwidth + ingress path.** htb/tbf stacking under netem for `rate`. IFB setup in the player-shaper for downstream impairment.
*Accept:* a `rate`-limited scenario throttles measurably; a `degrade_player: true` scenario impairs the *pull* path while the origin stays healthy.

**Phase 4 — Encoder-level chaos.** `restart_encoder`, `kill_encoder`, `av_desync` (itsoffset), `pts_jump`, `keyframe_misalign`.
*Accept:* `reconnect-storm` produces `EXT-X-DISCONTINUITY` tags in the manifest at each restart; `av-desync` produces measurable A/V offset.

**Phase 5 — SCTE-35 + ad splice.** `scte` module wrapping threefive to author `time_signal`/`splice_insert` on cadence and mux into the mpegts path; `misalign` support to offset splice points from GOP boundaries; optional `ad` block that encodes and splices a real ad segment at each break with an independently configurable profile (bitrate/resolution/GOP).
*Accept:* authored markers appear as `EXT-X-DATERANGE`/`CUE-OUT`/`CUE-IN` in the HLS manifest; `misalign: true` produces splice points that do not land on IDR frames; with `ad.enabled` and a mismatched profile, the manifest/segments show the profile change at the boundary.

**Phase 6 — Verification + reporting.** `verify` pulls the manifest, runs checks, and emits a JSON report; `run` exits non-zero on failure.
*Accept:* a report diffs authored vs. observed SCTE markers, flags segment-duration anomalies and discontinuities, and records join time + rebuffering counts.

## 9. Presets to ship

| Preset | What it exercises |
|---|---|
| `clean` | Baseline, no impairment — the control. |
| `flaky-wifi` | Variable delay + jitter with periodic loss bursts on the uplink. |
| `congested-uplink` | Periodic `rate` collapse (htb-backed) — encoder starvation. |
| `reconnect-storm` | Kill/respawn encoder every N seconds — discontinuity handling. |
| `av-desync` | `itsoffset` on one input — A/V drift handling. |
| `pts-jump` | Timestamp discontinuities — player/packager PTS handling. |
| `bad-client` | Healthy origin, impaired *player pull* path — join time + rebuffering. |
| `splice-misalign` | SCTE-35 splice points deliberately off GOP boundaries — ad-transition breakage. |
| `scte-cadence` | Well-formed SCTE-35 on a fixed cadence — marker pass-through fidelity. |
| `complex-source` | High-complexity VBR content colliding with a modest `rate` cap — tests the real downstream effect of content-driven bitrate spikes (uplink bursts, variable segment sizes, ABR noise). |
| `ad-boundary-mismatch` | Splices an ad encoded to a different bitrate/resolution/GOP profile than the program — reproduces the visible quality step or stall at the content↔ad transition, a common SSAI QC complaint. |

## 10. Verification detail

The verifier is what makes this a test tool rather than a chaos generator. For each run it pulls the resulting HLS/DASH manifest and compares reality against the scenario's intent:

- **Segment duration** — every segment should be a multiple of GOP duration; flag drift.
- **Discontinuity tags** — `EXT-X-DISCONTINUITY` should appear at each encoder restart/PTS jump and nowhere else.
- **SCTE markers** — decode manifest markers (threefive) and diff against what was authored: count, timing, type, and whether each splice landed on a keyframe.
- **Join time** — time from player start to first rendered frame (OCR the burned-in timecode, or measure first-frame wallclock).
- **Rebuffering** — count/duration of stalls during a fixed playback window.
- **Ad-boundary continuity** — when `ad.enabled`, check for rebuffering or an abrupt profile/quality step at the content↔ad splice (and back), and confirm the ad segments land on keyframe boundaries.

Report is JSON (machine-diffable across runs) with a human printer via `streamwreck report`.

## 11. Reference commands (starting points, not final)

**Encoder (Phase 1):**
```bash
ffmpeg -re -f lavfi -i "testsrc2=size=1280x720:rate=30" \
  -f lavfi -i "sine=frequency=1000:sample_rate=48000" \
  -vf "drawtext=text='%{pts\:hms}':fontsize=48:x=10:y=10:box=1:boxcolor=black:fontcolor=white" \
  -c:v libx264 -preset veryfast -tune zerolatency \
  -b:v 3M -maxrate 3M -bufsize 6M \
  -g 60 -keyint_min 60 -sc_threshold 0 \
  -c:a aac -b:a 128k \
  -f flv rtmp://ingest/live/stream
```

**Egress shaping (Phase 2):**
```bash
tc qdisc add dev eth0 root netem delay 80ms 30ms distribution normal loss 3% rate 2500kbit
tc qdisc change dev eth0 root netem delay 200ms 80ms loss 15% rate 800kbit
tc qdisc del dev eth0 root
```

**Ingress path via IFB (Phase 3):**
```bash
ip link add ifb0 type ifb && ip link set ifb0 up
tc qdisc add dev eth0 handle ffff: ingress
tc filter add dev eth0 parent ffff: u32 match u32 0 0 action mirred egress redirect dev ifb0
tc qdisc add dev ifb0 root netem delay 100ms loss 5%
```

**Accurate bandwidth (Phase 3):** attach `htb` (or `tbf`) as the root qdisc with the rate cap, then netem as a child for loss/delay — don't rely on netem `rate` alone.

## 12. Open decisions for the implementer

- **HLS vs DASH first** for verification — HLS is simpler to parse; start there.
- **threefive muxing path** — confirm whether markers are injected into the ffmpeg mpegts output pre-ingest or authored at MediaMTX; prototype both and pick the one that lands markers reliably in the manifest.
- **Timecode measurement** — OCR of burned-in PTS vs. a lighter first-frame wallclock signal; OCR is more accurate but heavier.

---

*Constraints in §4.2 are load-bearing. If a phase seems to require violating one (shaping the host, baking NET_ADMIN into the encoder, retargeting libx264 bitrate live, shaping ingress without IFB), stop and reconsider the approach — those are exactly the traps this design avoids.*
