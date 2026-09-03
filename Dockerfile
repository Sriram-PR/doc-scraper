# Build context is prepared by goreleaser dockers_v2: pre-built binaries land
# in per-platform directories, selected via TARGETPLATFORM. No Go toolchain
# here. distroless/static ships CA certificates, which the crawler needs for
# HTTPS.
FROM gcr.io/distroless/static:nonroot

ARG TARGETPLATFORM

COPY $TARGETPLATFORM/doc-scraper /usr/local/bin/doc-scraper

WORKDIR /data

ENTRYPOINT ["/usr/local/bin/doc-scraper"]
