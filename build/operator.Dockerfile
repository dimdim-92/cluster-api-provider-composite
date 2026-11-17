# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
ARG builder_image=docker.io/library/golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280
ARG base_image=gcr.io/distroless/static:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
FROM --platform=$BUILDPLATFORM ${builder_image} AS builder
ARG TARGETOS
ARG TARGETARCH
ARG version=dev
WORKDIR /src
COPY go.mod go.sum ./
COPY api/go.mod api/go.sum ./api/
RUN --mount=type=cache,target=/go/pkg/mod GOPROXY=https://proxy.golang.org,direct go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.Version=${version}" -o /manager ./cmd/operator

FROM ${base_image}
ARG version=dev
ARG revision=unknown
LABEL org.opencontainers.image.source="https://github.com/numspot/cluster-api-provider-composite" \
      org.opencontainers.image.version="${version}" \
      org.opencontainers.image.revision="${revision}"
COPY --from=builder /manager /manager
USER 65532:65532
EXPOSE 2112 5555
ENTRYPOINT ["/manager"]
