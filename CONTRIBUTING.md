# Contributing

Bug reports, ideas and pull requests are welcome. A security problem goes
through private reporting instead, as [SECURITY.md](SECURITY.md) describes —
never a public issue.

## Reporting a bug

Use the bug form. Say which version is running — the about box at the foot of
the preferences sheet shows it, as does `/api/info` — which browser and
device, and for anything to do with playback, the server's log around the
moment from a server run with `-debug`.

**Leave out the names of your files and folders.** Describe their shape
instead: "an MKV holding HEVC and two soundtracks", "a film split over a
multi-part RAR set".

## Building and testing

Everything builds inside Docker; the host needs nothing else:

```sh
make test     # frontend type check, tests and build; go vet for Linux, macOS
              # and Windows; go test -race with ffmpeg present
make build    # extracts the static Linux binary to ./mediator
```

[AGENTS.md](AGENTS.md) explains how the code is organised and why, and which
rules each part keeps. Read the part you are changing before changing it.

## Pull requests

- One change per pull request. A fix comes with a test that fails without it.
- `make test` passes.
- The docs change with the code: `README.md` for what it does and how it is
  run (features, flags, endpoints), `AGENTS.md` for how it works and why,
  written in the present tense.
- New source files start with `SPDX-License-Identifier: MIT` in their own
  comment syntax (AGENTS.md, "License headers").
- Nothing committed may name what anybody's library holds: not in tests,
  fixtures, comments, docs or commit messages, and screenshots and sample
  media are never committed. Invent names that keep the shape under test
  (AGENTS.md, "Writing about this project"); `make names` helps find a slip.

By contributing you agree that your contribution is licensed under the
[MIT License](LICENSE).
