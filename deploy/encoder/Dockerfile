# Encoder/player image: ffmpeg + a TTF font (for the drawtext PTS overlay) +
# curl (for the verifier's manifest pulls from inside the player netns).
# Deliberately unprivileged: NO tc, NO NET_ADMIN. Shaping is a sidecar concern.
FROM jrottenberg/ffmpeg:7.1-ubuntu

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        curl \
        fonts-dejavu-core \
    && rm -rf /var/lib/apt/lists/*

# jrottenberg/ffmpeg sets ENTRYPOINT to ffmpeg; clear it so compose `command:`
# (sleep infinity) and `exec sh -c ...` work as plain processes.
ENTRYPOINT []
