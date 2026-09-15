# RepeaterTastic in a container: the same static binary as `make build`, run as an unprivileged
# user in a distroless image. The web GUI is embedded (internal/web/dist is committed), so no
# Node toolchain is needed here. See deploy/docker-compose.example.yml and the README.

FROM golang:1.24-bookworm AS build
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
# distroless has no shell to chown a volume with: prepare /data here, owned by nonroot (65532).
RUN mkdir -p /data && chown 65532:65532 /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/repeatertastic /repeatertastic
COPY --from=build /out/kisstool /kisstool
COPY --from=build --chown=65532:65532 /data /data
# Config and state (identity keys, chats, node DB) live on the volume. A missing config file
# starts the setup wizard in the web GUI, which saves it there.
ENV REPEATERTASTIC_CONFIG=/data/repeatertastic.yaml \
    REPEATERTASTIC_STATE_DIR=/data
VOLUME /data
# 8080 web GUI and API; 4403+ one Meshtastic client-API port per identity.
EXPOSE 8080 4403
HEALTHCHECK --interval=60s --timeout=5s --start-period=20s CMD ["/repeatertastic", "healthcheck"]
ENTRYPOINT ["/repeatertastic"]
