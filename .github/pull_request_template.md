## What changes

<!-- Describe the problem and the resulting behavior. Link a related issue if any. -->

## Validation

<!-- List checks actually run and their results. For a bug fix, show that its
regression test fails without the fix. For docs-only changes, check commands
and links; mark application tests as not applicable rather than claiming a run. -->

## Checklist

- [ ] `make test` passes, or this is docs-only and validation is explained above
- [ ] A bug fix comes with a test that fails without it, or no behavior is changed
- [ ] `README.md`, affected guides and the `docs/` chapter (or `AGENTS.md`) describe the change
- [ ] New source files carry `SPDX-License-Identifier: MIT`
- [ ] API changes include regenerated `web/src/types.gen.ts`, or the API is unchanged
- [ ] No real media names, personal paths, credentials or library screenshots in this change
