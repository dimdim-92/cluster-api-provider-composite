# Tilt dev image: host-built binary wrapped in delve for step debugging.
FROM docker.io/library/golang:1.27.3-alpine@sha256:d530cf60723abf1ffae0cd16bb546b379ec0550d2f3815e5b16ed69dbc00fba4
RUN go install github.com/go-delve/delve/cmd/dlv@v1.26.0
COPY capi-composite-controller /usr/local/bin/capi-composite-controller
EXPOSE 5555 2112 50100
ENV HOME=/tmp
USER 65532:65532
ENTRYPOINT ["/go/bin/dlv", "--listen=0.0.0.0:50100", "--continue", "--api-version=2", \
            "--headless=true", "--only-same-user=false", "--accept-multiclient", \
            "--check-go-version=false", "exec", "/usr/local/bin/capi-composite-controller"]
