# Contributing to Mediator

You can help with bug reports, documentation, browser and receiver testing,
or code. Small, focused changes are welcome. Everyone taking part follows the
[code of conduct](CODE_OF_CONDUCT.md).

For usage questions, see [Support](SUPPORT.md). Report security problems
[privately](SECURITY.md), never in a public issue.

## Find a place to start

- Improve an instruction you found confusing while installing or using Mediator.
- Reproduce an [open bug](https://github.com/JohanLindvall/Mediator/labels/bug)
  and add your browser, platform and redacted results.
- Browse [good first issues](https://github.com/JohanLindvall/Mediator/labels/good%20first%20issue)
  or [help wanted](https://github.com/JohanLindvall/Mediator/labels/help%20wanted).
  If none fit, a documentation fix is a useful first contribution.
- For a substantial feature, open a [feature request](https://github.com/JohanLindvall/Mediator/issues/new?template=feature_request.yml)
  describing the problem before investing in an implementation.

You do not need an issue for a typo fix or a small, clear improvement.

## Make your first change

1. Fork the repository on GitHub and clone your fork.

   ```sh
   git clone https://github.com/YOUR-USERNAME/Mediator.git
   cd Mediator
   git switch -c improve-setup
   ```

2. Install **Git, Make and Docker with BuildKit**. Build and test commands
   run inside Docker; local Go and Node are optional.

   ```sh
   make help
   make test
   ```

3. Read [AGENTS.md](AGENTS.md) and the [architecture chapter](docs/README.md#architecture)
   for the code you are changing. Use the [development guide](docs/development.md)
   for the live frontend workflow, targeted tests and native builds.
4. Make one focused change. Add a regression test for a bug fix and update
   the user-facing docs and relevant chapter in the same change.
5. Run `make test` after code changes, `make names` for fixtures or
   measurements, and `git diff --check`. Inspect `git diff` before committing.
   For documentation-only changes, check commands and links and say in the
   pull request which checks you ran.
6. Commit, push your branch to your fork and open a pull request against
   `main`. A draft is welcome when you want feedback before finishing.

## Project conventions

- **Keep docs with code.** Update `README.md` for features, flags, endpoints
  and layout; update the appropriate `docs/` guide and architecture chapter
  for behavior and reasoning. A rule for the whole repository belongs in
  `AGENTS.md`. Describe current behavior in the present tense.
- **Keep the API generated.** Go structs define the wire format. Follow
  [the API model rules](AGENTS.md#generated-typescript-api-model--single-source-of-truth)
  and run `make generate` when changing it; include both Go and generated
  TypeScript changes.
- **Keep formatting consistent.** Use `gofmt` for Go and the existing
  TypeScript style. [`.editorconfig`](.editorconfig) sets whitespace defaults.
- **Keep the license header.** Every new source file starts with
  `SPDX-License-Identifier: MIT` in its own comment syntax. See
  [license headers](AGENTS.md#license-headers) for placement.
- **Keep real libraries private.** No real media names, personal paths,
  screenshots, recordings, sample media or binary fixtures in commits.
  Invent names that preserve the shape under test. Check comments, docs and
  commit messages too; [the naming rules](AGENTS.md#writing-about-this-project)
  explain how. `make names` produces questions for a human to review, not a
  pass/fail verdict.

## A useful pull request

Explain the problem, what changes for the user, and how you checked it.
Link a related issue if there is one. For a bug fix, show that its regression
test fails without the fix. For a concurrency fault, follow the deterministic
ordering tests in `AGENTS.md` rather than relying on repeated race attempts.
For a documentation-only change, write “not applicable” for code checks.

Keep unrelated refactors separate so a reviewer can assess the change.
Respond to review in the same pull request; no particular commit-message
format or squash workflow is required.

By contributing you agree that your contribution is licensed under the
[MIT License](LICENSE).
