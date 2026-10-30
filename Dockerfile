# syntax=docker/dockerfile:1
# One static binary on scratch, run as non-root (ADR-0006). The build stages
# run on the build machine's platform and cross-compile, so an arm64 image
# needs no emulation.

# The web UI is plain static files, so it's built once for every platform.
FROM --platform=$BUILDPLATFORM oven/bun:1.4 AS web
WORKDIR /src
COPY web/package.json web/bun.lock web/
RUN cd web && bun install --frozen-lockfile
COPY web/ web/
RUN bun web/build.ts

FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY server/ ./
COPY --from=web /src/server/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/dyno . \
 && mkdir /out/data

FROM scratch
COPY --from=build /out/dyno /dyno
# Results live in /data/dyno.db; mount a volume there to keep them.
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV DYNO_DATA=/data
# Keeps a burst from leaving the heap high (ADR-0006). Change only with measurements.
ENV GOMEMLIMIT=64MiB
EXPOSE 3966
VOLUME /data
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s CMD ["/dyno", "-healthcheck"]
ENTRYPOINT ["/dyno"]
