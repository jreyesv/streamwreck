# Shaper sidecar image: the network-impairment toolbox. iproute2 provides
# tc/netem/htb/ifb; python3 + threefive author and decode SCTE-35. This image
# gets NET_ADMIN in compose; the encoder/player images never do.
#
# The streamwreck controller runs on the HOST and drives this container via
# `docker compose exec`, so the Go binary is intentionally NOT baked in here.
FROM python:3.12-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        iproute2 \
        iputils-ping \
        procps \
    && rm -rf /var/lib/apt/lists/* \
    && pip install --no-cache-dir threefive

# Load the ifb kernel module hint (module itself is provided by the host kernel;
# `ip link add ... type ifb` is what actually creates the device at runtime).
