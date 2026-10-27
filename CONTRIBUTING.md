# Contributing to Portico

Portico is pre-release and built in the open. Issues and pull requests are
welcome; this page says how the project works so a contribution fits.

## Ground rules

- **English everywhere in the repository**: code, comments, docs, commit
  messages, UI strings. (`docs/history/` keeps the original Italian
  analysis as provenance.)
- **Security rules that shape the code** (see `CLAUDE.md` and the design
  document): the public path fails closed, Prism Central local accounts
  never authenticate from outside, Portico works through its own service
  account and never stores a human administrator's credentials, secrets
  at rest are sealed with a master key that never leaves the appliance.
  A change that weakens one of these needs an ADR, not just a PR.
- **No secrets, ever**: no passwords, API keys, tokens, session ids, CSRF
  tokens or `Authorization` values in code, tests, logs, fixtures, docs or
  commits. Lab credentials live outside the repository.
- **Nutanix documentation is copyrighted** and stays out of the repository
  (`kb/` is ignored). Quote endpoints and behaviour, not pages.

## How changes are made

1. **Plan before code.** A milestone starts from the design document
   (`docs/superpowers/specs/`) and a written plan (`docs/superpowers/plans/`)
   with tasks small enough to carry their own test.
2. **Test first.** Every behaviour change starts with a test watched failing,
   then the smallest code that makes it pass; `go test ./...` stays green.
3. **Measure, then decide.** Anything that depends on how Prism Central or
   Keycloak behaves is measured on a lab and written up in `docs/lab/`
   (`TEMPLATE.md`), with the version it was measured on and how to measure
   again after an upgrade. Decisions that change the design go to
   `docs/adr/`.
4. **Small PRs**, one concern each, with the test plan in the description.
   The CI runs `gofmt`, `go vet`, `go build`, `go test ./...` and the
   offline checks on the lab scripts (`lab/test_scripts.sh`, shellcheck).

## Releases

A release is an annotated tag `vX.Y.Z` on `main`; a rehearsal before it
is `vX.Y.Z-rc.N` (SemVer pre-release, with the dot before the number).
Both run the image workflow, which needs `docs/releases/vX.Y.Z.md` (an
`-rc.N` tag shares its release's notes) and makes a draft release, a
pre-release when the tag has a suffix. A maintainer reads the draft and
publishes it.

## Running the checks locally

```
gofmt -l cmd internal
go vet ./...
go test ./...
lab/test_scripts.sh
```

Go 1.27 or later; the lab script checks need bash 5.2, jq and shellcheck.

## The lab

`lab/` holds the scripts that drive a real Prism Central and a lab VM
(Keycloak, PostgreSQL, the Portico binary). They read their environment
from `lab/env.sh` and their credentials from a directory outside the
repository. `docs/lab/lab-inventory.md` lists what the lab creates and how
to remove it. You do not need a lab to contribute to the Go code: the
tests run against fakes of Prism Central and Keycloak.

Hardware, lab access and field experience help as much as code: see
[Supporting the project](README.md#supporting-the-project) in the README.
