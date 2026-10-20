# Project maintenance

[Documentation](README.md) · [Contributing](../CONTRIBUTING.md)

This chapter covers the repository's entry points: `README.md`, user guides,
community files, `.github/`, `compose.yaml`, `.env.example`, `.editorconfig`
and the Makefile's command help.

## Help a visitor get started

The README introduces the project, gives a working Docker command and links
to focused guides. The full flag and API tables stay in expandable sections
of the README so their existing reference location remains stable. Detailed
feature behavior belongs in `features.md`; installation, configuration,
networking and troubleshooting each have their own guide. `docs/README.md`
is the navigation index for users and contributors.

Keep examples copyable, with existing-path checks, explicit platform
requirements and an explanation of where state lives. The quick start binds
to loopback, mounts media read-only and passes `-lock`; it does not require a
new user to grant deletion or directory-management access just to try playback.
LAN access and writable libraries are documented choices. Compose uses a named
volume and refuses to create a missing bind source. `MEDIA_DIR` is required;
the other environment variables have defaults.

Personal `.env` and Compose override files are ignored by Git and the Docker
build context. Keep `.env.example` generic. EditorConfig reflects existing
formatting: two spaces in TypeScript and configuration, tabs in Go and Make.
`make help` runs without Docker; `make` still builds the binary by default.

Do not add screenshots, recordings, images or sample media to make the
landing page more attractive. The repository's privacy rule applies to
documentation and promotion too; describe features and use invented names.

## Support and contributions

`SUPPORT.md` directs questions, bugs, feature requests and private security
reports to the appropriate place. Questions use an issue form because this
repository does not require Discussions to be enabled. Forms ask for a
version, environment and reproducible steps without requiring users to
understand the implementation. Logs must omit media names and credentials;
`/api/info` may carry a media token, so ask for the build fields rather than
the complete response.

Keep issue and pull-request templates consistent with `CONTRIBUTING.md` and
`AGENTS.md`. Documentation-only changes record their relevant checks without
claiming to have run application tests. Useful first issues have a clear
problem, relevant code pointers and a way to verify the result; use `good first
issue` and `help wanted` for work with that scope.

## Dependency updates and releases

`.github/dependabot.yml` checks Go modules, npm, Docker base images and GitHub
Actions weekly. Minor and patch updates are grouped per ecosystem and each
ecosystem permits at most two open version-update pull requests. Major
updates remain separate. Review dependency changes and run the normal CI;
the configuration does not enable automatic merging. Preserve intentional
pins and replacements, including the RAR decoder fix documented in `go.mod`.
Check Go and Node requirements in the guides when their build images change.

`.github/release.yml` groups generated release notes by breaking changes,
features, fixes, documentation, dependencies and other work. The catch-all
keeps unlabeled pull requests visible. It configures the **Generate release
notes** button; it does not publish a release. Review generated notes for
useful user-facing descriptions and document any required upgrade steps.

For a release, update both `version` and `date-released` in `CITATION.cff`,
verify CI, tag the intended commit as `vX.Y.Z`, and publish notes for that
tag. The existing Docker workflow publishes versioned image tags after its
tests pass. `latest` follows main; a release tag does not move it. Do not
advertise downloadable native binaries until release assets exist.

## Check onboarding changes

Run `make help` and `git diff --check`. Check Markdown links and anchors after
moving a section; keep the documentation index current. Validate Compose with
an existing temporary media directory:

```sh
MEDIA_DIR=/absolute/path/to/test-media docker compose config --quiet
```

For Compose behavior changes, start an isolated project against synthetic
media and a local image; confirm `/api/info`, the read-only mount, locked
preferences, data persistence and shutdown. Never use the owner's library
for a public fixture. The CI test job also validates Compose configuration.
Changes to application code still need `make test` and the checks in their
architecture chapter.

The structure follows GitHub's guidance on
[READMEs](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-readmes),
[Dependabot configuration](https://docs.github.com/en/code-security/reference/supply-chain-security/dependabot-options-reference)
and [generated release notes](https://docs.github.com/en/repositories/releasing-projects-on-github/automatically-generated-release-notes).
