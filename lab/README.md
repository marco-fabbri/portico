# Lab scripts

Shell helpers used for the Milestone 0 measurements on the lab cluster
NTNX-01. They are **lab only**: nothing here is product code, and nothing
here runs on the appliance.

- `env.sh` — source it first. Exports `PC_URL`, `PE_URL`, `PC_USER`,
  `PC_PASS`, `LAB_VM_IP`, `LAB_PRISM_HOST`, `LAB_ID_HOST`. Reads the password
  from `~/portico-secrets/prism-pw` and stops if the file is missing.
- `pc.sh METHOD PATH [JSON_FILE]` — calls the Prism Central API with Basic
  auth. Body on stdout, `HTTP <code>` on stderr. Adds `NTNX-Request-Id` on
  v4 writes and `If-Match` when `IF_MATCH` is set.
- `test_env.sh` — checks `env.sh`.

Secrets rule: credentials live in `~/portico-secrets/` (mode 0600). Scripts
read them from there and never print, log, or copy them under the
repository.
