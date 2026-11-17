# Tilt dev image: host-built binary wrapped in delve for step debugging.
FROM docker.io/library/golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280
RUN go install github.com/go-delve/delve/cmd/dlv@v1.26.0
COPY capi-composite-controller /usr/local/bin/capi-composite-controller
EXPOSE 5555 2112 50100
ENV HOME=/tmp
USER 65532:65532
ENTRYPOINT ["/go/bin/dlv", "--listen=0.0.0.0:50100", "--continue", "--api-version=2", \
            "--headless=true", "--only-same-user=false", "--accept-multiclient", \
            "--check-go-version=false", "exec", "/usr/local/bin/capi-composite-controller"]
