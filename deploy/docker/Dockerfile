# syntax=docker/dockerfile:1
# The single sluice binary (api | worker | mockpay | migrate | seed | rush | probe)
# on distroless static. Build from the repo root:
#   docker build -f deploy/docker/Dockerfile --build-arg VERSION=dev --build-arg COMMIT=$(git rev-parse --short HEAD) .

ARG GO_VERSION=1.27.4

FROM golang:${GO_VERSION} AS build
WORKDIR /src
# Dependencies first, so code edits reuse the module layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY db ./db
ARG VERSION=dev
ARG COMMIT=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w -X github.com/sheshakanthra/sluice/internal/buildinfo.Version=${VERSION} -X github.com/sheshakanthra/sluice/internal/buildinfo.Commit=${COMMIT}" \
      -o /out/sluice ./cmd/sluice

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sluice /usr/local/bin/sluice
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sluice"]
CMD ["version"]
