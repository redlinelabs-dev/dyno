# dyno build and checks. Go is found on PATH, else in ~/.local/go (see CLAUDE.md).
GO      ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.local/go/bin/go)
GOFMT   ?= $(dir $(GO))gofmt
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
IMAGE   ?= dyno:dev

export CGO_ENABLED = 0

.PHONY: web build loadgen check fmt vet test webcheck footprint bench image image-multiarch clean

## web: the web UI, built by Bun into the folder the server embeds
web: web/node_modules
	bun web/build.ts

web/node_modules: web/package.json
	cd web && bun install --frozen-lockfile
	@touch $@

## build: the static server binary at bin/dyno, web UI included
build: web
	cd server && $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o ../bin/dyno .

## loadgen: the bench load generator the footprint check drives
loadgen:
	cd bench/loadgen && $(GO) build -trimpath -o ../../bin/loadgen .

## check: formatting, vet, race tests, web typecheck and tests, quick footprint check (~2 min)
check: fmt vet test webcheck footprint

fmt:
	@out=$$($(GOFMT) -l server bench/loadgen); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet: web
	cd server && $(GO) vet ./...

# The race detector needs cgo.
test: web
	cd server && CGO_ENABLED=1 $(GO) test -race ./...

webcheck: web/node_modules
	cd web && bunx tsc --noEmit && bun test

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
	rm -rf bin server/internal/webui/dist
