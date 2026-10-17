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
- Lab findings go in `docs/lab/` as short write-ups with what was measured,
  on which version, and how to re-measure after an upgrade.

## Out-of-repo material

- Lab credentials: `~/portico-secrets/` (e.g. `prism-pw`, same password for
  Prism Central and Prism Element, user `admin`). Scripts read from there.
  Never copy, print, or log them. Never commit them.
- Nutanix documentation (Projects Guide pc.7.6, Security Guide 7.6, v4 API
  reference): `~/portico-kb/`. Copyrighted; never commit. Portal pages are not
  readable from the CLI, so this folder is the source for API details.

## Platform baseline

- Prism Central pc.7.6 and later, Projects 2.0 as the tenant primitive.
- Lab: cluster NTNX-01, Prism Central `https://10.23.210.175:9440`, VLAN 210
  `10.23.210.0/24`, gateway `.254`, free IPs from `.190` (check with arping).

## Security rules that shape the code

- The public path fails closed: no Portico session, no request to Prism
  Central. Local Prism Central accounts never authenticate from outside.
- Portico works through its own `portico` service account in Prism Central
  and never stores a human administrator's credentials.
- Secrets at rest are encrypted; the master key never leaves the appliance.
