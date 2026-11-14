# dyno build and checks. Go is found on PATH, else in ~/.local/go (see CLAUDE.md).
GO      ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.local/go/bin/go)
GOFMT   ?= $(dir $(GO))gofmt
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
IMAGE   ?= dyno:dev

export CGO_ENABLED = 0

.PHONY: build loadgen check fmt vet test footprint bench image image-multiarch clean

## build: the static server binary at bin/dyno
build:
	cd server && $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o ../bin/dyno .

## loadgen: the bench load generator the footprint check drives
loadgen:
	cd bench/loadgen && $(GO) build -trimpath -o ../../bin/loadgen .

## check: formatting, vet, race tests and the quick footprint check (~2 min)
check: fmt vet test footprint

fmt:
	@out=$$($(GOFMT) -l server bench/loadgen); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	cd server && $(GO) vet ./...

# The race detector needs cgo.
test:
	cd server && CGO_ENABLED=1 $(GO) test -race ./...

footprint: build loadgen
	bun scripts/footprint.ts

## bench: the full footprint run with the 12-minute soak (~15 min); paste its table into the PR
bench: build loadgen
	bun scripts/footprint.ts --full

## image: container image for this machine (needs Docker)
image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

## image-multiarch: build for amd64 and arm64 to prove both compile; add --push to publish
image-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t $(IMAGE) .

clean:
	rm -rf bin
