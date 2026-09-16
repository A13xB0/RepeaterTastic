# RepeaterTastic in a container, with the meshtasticd its nodes run on: the same static binary as
# `make build` on the official meshtasticd image, run as an unprivileged user. The web GUI is
# embedded (internal/web/dist is committed), so no Node toolchain is needed here. See
# deploy/docker-compose.example.yml and the README.

# Go cross-compiles, so the build stage runs natively whatever the target platform.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The map tile key comes in as a BuildKit secret so it never lands in an image layer or the
# build history: docker build --secret id=map_api_key,env=CARTO_API_KEY .
RUN --mount=type=secret,id=map_api_key \
    KEY="$(cat /run/secrets/map_api_key 2>/dev/null || true)"; \
    GOARM="${TARGETVARIANT#v}" CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -buildid= -X main.version=$VERSION -X main.mapAPIKey=$KEY" \
      -o /out/repeatertastic ./cmd/repeatertastic && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM="${TARGETVARIANT#v}" \
    go build -trimpath -ldflags "-s -w -buildid= -X main.version=$VERSION" -o /out/kisstool ./cmd/kisstool

# The relay persona and identities are meshtasticd instances the daemon starts in this container
# (hosted.meshtasticd left empty finds it on the PATH).
FROM meshtastic/meshtasticd:2.8.0.47db0e3-alpha-debian
RUN useradd --system --uid 65532 --home-dir /data --shell /usr/sbin/nologin repeatertastic && \
    usermod -aG dialout repeatertastic && \
    install -d -o repeatertastic -g repeatertastic /data
COPY --from=build /out/repeatertastic /usr/local/bin/repeatertastic
COPY --from=build /out/kisstool /usr/local/bin/kisstool
ENV REPEATERTASTIC_CONFIG=/data/repeatertastic.yaml \
    REPEATERTASTIC_STATE_DIR=/data
USER repeatertastic
VOLUME /data
# Config and state (identity keys, chats, node DB, the nodes' meshtasticd state) live on the volume.
# A missing config file starts the setup wizard in the web GUI, which saves it there.
# 8080 web GUI and API; 4403+ one Meshtastic client-API port per identity. The meshtasticd API
# ports (4500 up, 100 per radio) are for RepeaterTastic only: don't publish them.
EXPOSE 8080 4403
HEALTHCHECK --interval=60s --timeout=5s --start-period=20s CMD ["/usr/local/bin/repeatertastic", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/repeatertastic"]
# The base image's command starts its own meshtasticd: not here.
CMD []
