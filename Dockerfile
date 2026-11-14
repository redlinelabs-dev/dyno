# syntax=docker/dockerfile:1
# One static binary on scratch, run as non-root (ADR-0006). The build stage
# runs on the build machine's platform and cross-compiles, so an arm64 image
# needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/dyno .

FROM scratch
COPY --from=build /out/dyno /dyno
USER 65532:65532
# Keeps a burst from leaving the heap high (ADR-0006). Change only with measurements.
ENV GOMEMLIMIT=64MiB
EXPOSE 3966
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s CMD ["/dyno", "-healthcheck"]
ENTRYPOINT ["/dyno"]
