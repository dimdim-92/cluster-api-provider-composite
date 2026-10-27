# Local image for an explicitly host-built Linux binary.
FROM gcr.io/distroless/static:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
COPY capi-composite-controller /usr/local/bin/capi-composite-controller
EXPOSE 2112 5555
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/capi-composite-controller"]
