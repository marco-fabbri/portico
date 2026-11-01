# Portico — project rules

## Language

Everything in this repository is in **English**: code, comments, documentation,
commit messages, UI strings, file names. The conversation with Marco is in
Italian; nothing Italian goes on disk here. The only exception is
`docs/history/`, which preserves the original Italian analysis as provenance.

## Working style

- Plan before code. Marco is an infrastructure architect, not a developer:
  explain non-obvious choices and their trade-offs.
- Design document: `docs/superpowers/specs/2026-10-17-portico-design.md`.
  Update it when a decision changes; record the reasoning in `docs/adr/`.
- Lab findings are short write-ups with what was measured, on which
  version, and how to re-measure after an upgrade. They live outside the
  repository (below), never in it.

## Out-of-repo material

- Lab credentials: `~/portico-secrets/` (e.g. `prism-pw`, same password for
  Prism Central and Prism Element, user `admin`). Scripts read from there.
  Never copy, print, or log them. Never commit them.
- **The lab itself**: `~/portico-secrets/portico-lab/` holds the scripts,
  the write-ups, the plans and the lab note of the history, symlinked
  into the checkout at the paths `.gitignore` lists. The lab is a borrowed company cluster: nothing that
  describes it goes on disk here. No addresses, VLANs, host or cluster
  names, object ids, firewall models, run numbers. Documentation uses the
  RFC 5737 ranges and `example` names; tests use `10.0.x.x` fixtures. The
  CI refuses the lab's identifiers (`scripts/check-repo.sh`).
- Nutanix documentation (Projects Guide pc.7.6, Security Guide 7.6, v4 API
  reference): `kb/` (gitignored). Copyrighted; never commit. Portal pages are not
  readable from the CLI, so this folder is the source for API details.

## Platform baseline

- Prism Central pc.7.6 and later, Projects 2.0 as the tenant primitive.
- The lab's addresses and names: `~/portico-secrets/portico-lab/README.md`.

## Security rules that shape the code

- The public path fails closed: no Portico session, no request to Prism
  Central. Local Prism Central accounts never authenticate from outside.
- Portico works through its own `portico` service account in Prism Central
  and never stores a human administrator's credentials.
- Secrets at rest are encrypted; the master key never leaves the appliance.
