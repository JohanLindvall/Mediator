# The entire build runs in here: API model generation, frontend, checks and
# the Go binary. The host needs Docker (BuildKit), nothing else — no local Go,
# Node or npm packages are used, and no locally generated files enter the
# image (web/dist, web/node_modules and web/src/types.gen.ts are dockerignored).

# --- Go sources, shared by the gen / build / check stages -------------------
FROM golang:1.26-alpine AS gosrc
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Explicit copies so frontend edits don't bust the Go layer cache.
# A new top-level Go package needs a COPY line here.
COPY main.go ./
COPY cmd/ cmd/
COPY internal/ internal/

# --- TypeScript API model ----------------------------------------------------
# Regenerated on every build from the Go types; the checked-in copy is never
# used. Identical output keeps the frontend stage cache warm.
FROM gosrc AS gen
RUN go run ./cmd/gen-ts -out /types.gen.ts

# --- frontend build ----------------------------------------------------------
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
COPY --from=gen /types.gen.ts src/types.gen.ts
# The frontend's own tests run here, with node's test runner and no framework:
# they cover the two predicates that decide which playback route a viewer
# takes, and a wrong answer there is a file that will not play rather than
# something that merely looks wrong.
RUN npm test
RUN npm run build

# --- backend build -----------------------------------------------------------
FROM gosrc AS build
COPY --from=web /src/web/dist ./web/dist
# The sources are copied in, so there is no repository here for Go's own
# version stamping to read. What the Makefile could see is passed instead.
ARG VERSION=
ARG COMMIT=
ARG BUILT=
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w \
      -X github.com/JohanLindvall/Mediator/internal/server.buildVersion=${VERSION} \
      -X github.com/JohanLindvall/Mediator/internal/server.buildCommit=${COMMIT} \
      -X github.com/JohanLindvall/Mediator/internal/server.buildTime=${BUILT}" \
    -o /out/mediator .

# --- checks: `make vet` / `make test` build these targets ---------------------
FROM build AS vet
RUN go vet ./...

FROM vet AS test
# The race detector needs cgo, which needs a C toolchain in this stage only.
# The suite passes -race today; keeping it on is what stops an unlocked field
# from surviving until a user hits it.
RUN apk add --no-cache gcc musl-dev
RUN CGO_ENABLED=1 go test -race ./...

# --- BuildKit --output targets: `make build` / `make generate` ----------------
FROM scratch AS bin
COPY --from=build /out/mediator /mediator

FROM scratch AS types
COPY --from=gen /types.gen.ts /web/src/types.gen.ts

# --- runtime -------------------------------------------------------------------
# ffmpeg is optional: without it everything works except video thumbnails.
FROM alpine:3.22
# intel-media-driver is what lets conversions run on the graphics hardware
# rather than the processor, which is the difference between a 4K phone clip
# playing and buffering for ever (see hwaccel.go). ffmpeg here is built with
# VAAPI support but ships no driver, so without this the device node is
# present and useless. It is only reached when the container is given one —
# `--device /dev/dri` — and everything falls back to the processor when it
# is not.
RUN apk add --no-cache ffmpeg ca-certificates tzdata intel-media-driver
COPY --from=build /out/mediator /usr/local/bin/mediator

VOLUME /data
EXPOSE 8080
# /api/info answers the moment the listener is bound, before any scan: a
# container that cannot answer it is not serving.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/api/info || exit 1
ENTRYPOINT ["mediator", "-listen", ":8080", "-data", "/data"]
# Directories to scan/watch are passed as arguments:
#   docker run -v /your/media:/library -v mediator-data:/data -p 8080:8080 mediator /library
CMD ["/library"]
