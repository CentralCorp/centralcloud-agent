GO_IMAGE ?= golang:1.26.5
VERSION ?= dev
GO_RUN = docker run --rm -v $(CURDIR):/src -v centralcloud-go-cache:/go/pkg/mod -w /src $(GO_IMAGE)

.PHONY: fmt fmt-check vet lint test test-race build build-all docker-build compose-up compose-down
fmt:
	$(GO_RUN) gofmt -w cmd internal pkg
fmt-check:
	@test -z "$$($(GO_RUN) gofmt -l cmd internal pkg)"
vet:
	$(GO_RUN) go vet ./...
lint:
	docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:v2.11.4 golangci-lint run
test:
	$(GO_RUN) go test ./...
test-race:
	$(GO_RUN) go test -race ./...
build:
	$(GO_RUN) go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/centralcloud-agent ./cmd/agent
build-all:
	mkdir -p dist
	$(GO_RUN) sh -c 'CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/centralcloud-agent-linux-amd64 ./cmd/agent && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/centralcloud-agent-linux-arm64 ./cmd/agent'
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t centralcloud-node-agent:$(VERSION) .
compose-up:
	docker compose -f deploy/docker-compose/docker-compose.yml up -d --build
compose-down:
	docker compose -f deploy/docker-compose/docker-compose.yml down -v --remove-orphans
