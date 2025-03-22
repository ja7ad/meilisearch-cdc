# syntax=docker/dockerfile:1.7
# The build stage runs on the builder's platform and cross-compiles for the
# target one (no emulation), so multi-arch images build quickly.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/meilisearch-cdc ./cmd/meilisearch-cdc

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/meilisearch-cdc /usr/local/bin/meilisearch-cdc
ENV CDC_LISTEN=:8080 CDC_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/meilisearch-cdc"]
