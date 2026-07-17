# Encoder/player image: ffmpeg + a TTF font (for the drawtext PTS overlay) +
# curl (for the verifier's manifest pulls) + CA certs (for RTMPS/HTTPS to real
# platforms). Deliberately unprivileged: NO tc, NO NET_ADMIN.
#
# Base is plain Ubuntu (not a prebuilt ffmpeg image) so the image is
# multi-arch: on Apple Silicon it installs the NATIVE arm64 ffmpeg instead of
# an emulated x86_64 one. Emulated libx264 cannot sustain realtime 720p30
# encoding (~0.8x), which chronically underfeeds a real ingest and looks like
# broadcaster "starvation"; the native build runs several times realtime.
FROM ubuntu:24.04

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ffmpeg \
        curl \
        ca-certificates \
        fonts-dejavu-core \
    && rm -rf /var/lib/apt/lists/*
