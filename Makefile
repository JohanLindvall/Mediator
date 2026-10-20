# SPDX-License-Identifier: MIT

DOCKER ?= docker
IMAGE  ?= mediator:latest

## What is being built, for the version box. Read from the repository here,
## because the image is built from copied sources and has none.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
BUILT   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
STAMP   := --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILT=$(BUILT)

.PHONY: all build docker generate test vet run clean names help

all: build

help:
	@printf '%s\n' \
	  'Mediator commands (run from the repository root)' \
	  '' \
	  'Builds and checks use Docker with BuildKit; no local Go or Node needed:' \
	  '  make docker     Build the runtime image (mediator:latest)' \
	  '  make build      Extract ./mediator, a Linux binary' \
	  '  make generate   Regenerate the checked-in TypeScript API model' \
	  '  make test       Frontend tests/build, cross-platform vet, Go race tests' \
	  '  make vet        Frontend tests/build and cross-platform Go vet' \
	  '' \
	  'Host commands:' \
	  '  make run DIRS="/path/to/media"   Build and run on Linux' \
	  '  make names      Review tracked files for identifying media names' \
	  '  make clean      Remove the binary, frontend build and node_modules' \
	  '' \
	  'Overrides: DOCKER=docker IMAGE=mediator:latest VERSION=dev' \
	  'Setup and live frontend development: docs/development.md'

## Build and check targets use Docker (BuildKit) — no local Go, Node or npm
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
	rm -f mediator media
	rm -rf web/dist web/node_modules

# What in the tracked files could be a real name, for a reader to judge —
# see AGENTS.md, "Writing about this project". Runs on the host: grep only.
names:
	scripts/names.sh
