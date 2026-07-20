# syntax=docker/dockerfile:1.7
FROM golang:1.26.5 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/centralcloud-agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/centralcloud-agent /usr/local/bin/centralcloud-agent
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/centralcloud-agent"]
CMD ["-config", "/etc/centralcloud-agent/config.yaml"]
