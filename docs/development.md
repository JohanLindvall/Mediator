# Development

[Documentation](README.md) · [Contributing](../CONTRIBUTING.md) · [Repository rules](../AGENTS.md)

## Build and test in Docker

Install Git, Make and Docker with BuildKit. No local Go, Node or ffmpeg is
needed for the standard workflow:

```sh
git clone https://github.com/JohanLindvall/Mediator.git
cd Mediator
make help
make test
make docker
```

| Command | Result |
| --- | --- |
| `make docker` | Builds `mediator:latest`, including ffmpeg |
| `make build` | Extracts `./mediator`, a static Linux binary |
| `make generate` | Regenerates the checked-in `web/src/types.gen.ts` |
| `make test` | Frontend tests and build, `go vet` for Linux/macOS/Windows, and `go test -race ./...` with ffmpeg |
| `make vet` | Frontend tests and build, then the three-platform vet |
| `make names` | Lists possible identifying names in tracked files for manual review |
| `make clean` | Removes the binary, frontend build and `node_modules` |

`make` defaults to `make build`. Use `DOCKER=…` to select a Docker command
and `IMAGE=…` to name a local image. The Makefile stamps builds from Git;
`VERSION`, `COMMIT` and `BUILT` can be overridden.

The Dockerfile regenerates the TypeScript model before building the web app,
then embeds the web output in Go. Local `web/dist`, `web/node_modules` and
`web/src/types.gen.ts` are dockerignored so stale artifacts cannot enter the
image. The test stage runs as an ordinary user, with ffmpeg and ffprobe
installed so media integration and filesystem permission tests run.

`make build` produces a **Linux** executable even on a macOS or Windows Docker
host. Use the runtime image there, or build natively below. The current
runtime image includes an x86-only Intel driver; it targets amd64.

## Live frontend development

Install **Node 24 or newer** and npm on the host. Start a backend in one
terminal; media stays read-only and development state uses a separate volume:

```sh
make docker IMAGE=mediator:dev
docker run --rm -p 127.0.0.1:8080:8080 \
  --mount "type=bind,src=/path/to/your/media,dst=/library,readonly" \
  -v mediator-dev-data:/data \
  mediator:dev -lock /library
```

In a second terminal, from the repository root:

```sh
npm --prefix web ci
npm --prefix web run dev
```

Open [localhost:5173](http://localhost:5173). Vite reloads frontend changes and
proxies `/api` to the backend on port 8080. Backend changes need an image
rebuild and container restart; frontend changes do not. If you change the Go
API types, run `make generate` so the dev server sees the matching model.

## Native build

Install the Go version used in the [Dockerfile](../Dockerfile) (currently
1.26), Node 24+ and npm. Install `ffmpeg` and `ffprobe` for media features.
Run from the repository root:

```sh
go run ./cmd/gen-ts -out web/src/types.gen.ts
npm --prefix web ci
npm --prefix web run build
go build -o mediator .
./mediator -open -lock /path/to/your/media
```

On Windows, use `mediator.exe` as the output name. Go embeds `web/dist` from
`main.go`, so build the frontend before a native root-package build or test.
Rebuild it after frontend changes when checking the embedded app.

## Targeted checks

With the local toolchains installed and a current `web/dist`:

```sh
go test ./internal/library -run TestAlbums
npm --prefix web test
npm --prefix web run typecheck
git diff --check
```

Use `make test` before submitting code: it also catches portability problems
and runs the Go race detector. A race detector checks unsynchronized memory
access; it does not prove the ordering a concurrency regression needs.
[AGENTS.md](../AGENTS.md#testing-an-ordering-which-is-not-testing-a-race) describes
the deterministic test seams and how to prove a regression test.

Frontend tests use Node's test runner, without a framework. Lift pure behavior
out of DOM modules for testing. Runtime imports in tested modules need an
explicit `.ts` extension; type-only imports do not.

For optional deeper checks:

```sh
go test -coverprofile=/tmp/mediator-coverage.out ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
go test ./internal/library -run '^$' -fuzz FuzzMediaBoxBounds -fuzztime 30s
go test ./internal/library -run '^$' -fuzz FuzzZipDirectory -fuzztime 30s
npm --prefix web audit
```

Run race detection and coverage separately: combining both substantially
slows audio fingerprint tests. Fuzz targets use synthetic data; DLNA tests
use local simulated devices. Hardware playback and discovery also need
testing on real receivers.

## Find the right code

Start with the [layout](../README.md#layout), then the
[architecture index](README.md#architecture). Each chapter explains its code
and the invariants a change must preserve.

The Go structs are the source of truth for the API. Add named response types
and register new roots in `cmd/gen-ts` before running `make generate`; do not
edit `types.gen.ts` directly. See the
[generation rules](../AGENTS.md#generated-typescript-api-model--single-source-of-truth).

For contribution forms, dependency updates, releases and documentation
maintenance, see [Project maintenance](project.md).
