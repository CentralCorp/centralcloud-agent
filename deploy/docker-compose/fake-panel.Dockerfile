FROM golang:1.26.5 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /fake-panel ./cmd/fake-panel
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /fake-panel /fake-panel
USER 65532:65532
HEALTHCHECK --interval=2s --timeout=2s --retries=20 CMD ["/fake-panel", "healthcheck"]
ENTRYPOINT ["/fake-panel"]

