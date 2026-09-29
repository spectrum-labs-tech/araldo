# syntax=docker/dockerfile:1
# One image for every role: araldo server | worker | all | migrate (ADR 0002).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/spectrum-labs-tech/araldo/internal/buildinfo.Version=${VERSION}" \
      -o /out/araldo ./cmd/araldo

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/araldo /araldo
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/araldo"]
CMD ["server"]
