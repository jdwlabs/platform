# Build context is cli/ (see the release workflow's `context:` and
# `docker build -f cmd/truenas-prometheus-exporter/Dockerfile .` from cli/).
#
# Digests below were resolved live against the registries at the time this
# file was written (docker pull + docker inspect), not copied from memory —
# tools/check-image-pins.py's own docstring is explicit that it checks format
# only and never resolves a digest itself, so a stale one here would pass CI
# and only fail at image pull time.
FROM golang:1.27-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_ENABLED=0 is load-bearing, not defensive: the runtime stage below is
# distroless/static, which carries no libc for a cgo binary to dynamically
# link against.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/truenas-prometheus-exporter ./cmd/truenas-prometheus-exporter

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/truenas-prometheus-exporter /truenas-prometheus-exporter
USER nonroot:nonroot
EXPOSE 9124
ENTRYPOINT ["/truenas-prometheus-exporter"]
