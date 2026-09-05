DOCKER ?= docker
IMAGE  ?= mediator:latest

## What is being built, for the version box. Read from the repository here,
## because the image is built from copied sources and has none.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
BUILT   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
STAMP   := --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILT=$(BUILT)

.PHONY: all build docker generate test vet run clean names

all: build

## Everything builds inside Docker (BuildKit) — no local Go, Node or npm
## packages are needed or used. See the Dockerfile stages.

## Build the runtime image.
docker:
	$(DOCKER) build $(STAMP) -t $(IMAGE) .

## Build in Docker and extract the static linux binary to ./mediator.
build:
	$(DOCKER) build $(STAMP) --target bin --output type=local,dest=. .

## Regenerate web/src/types.gen.ts (kept in-tree only for `npm run dev`).
generate:
	$(DOCKER) build --target types --output type=local,dest=. .

## go vet, and go vet + go test, inside the image build.
vet:
	$(DOCKER) build --progress=plain --target vet .

test:
	$(DOCKER) build --progress=plain --target test .

## Example: make run DIRS="/media/movies /media/music"
run: build
	./mediator -listen :8080 $(DIRS)

clean:
	rm -rf media web/dist web/node_modules

# What in the tracked files could be a real name, for a reader to judge —
# see CLAUDE.md, "Writing about this project". Runs on the host: grep only.
names:
	scripts/names.sh
