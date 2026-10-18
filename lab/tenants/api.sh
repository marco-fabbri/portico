#!/usr/bin/env bash
# Call Portico's admin API on the lab VM. Lab only.
#   lab/tenants/api.sh METHOD PATH [JSON_FILE]     (PATH like /api/v1/tenants)
# Prints the body on stdout and "HTTP <code>" on stderr. The break-glass
# password (~/portico-secrets/portico-admin-pw, written by
# lab/portico/deploy.sh) goes to curl through a config file, never on the
# command line. The admin listener presents the lab certificate for the
# Prism host name, so the call resolves that name to the VM on port 8443.
set -euo pipefail
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_PRISM_HOST:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
method="${1:?usage: api.sh METHOD PATH [JSON_FILE]}"; path="${2:?}"; body="${3:-}"
[ -s "$S/portico-admin-pw" ] || { echo "api.sh: $S/portico-admin-pw missing; run lab/portico/deploy.sh" >&2; exit 1; }
umask 077
cfg=$(mktemp); trap 'rm -f "$cfg"' EXIT
{
  printf 'user = "admin:%s"\n' "$(tr -d '\r\n' < "$S/portico-admin-pw")"
  printf 'cacert = "%s"\n' "$S/lab-ca.crt"
  printf 'resolve = "%s:8443:%s"\n' "$LAB_PRISM_HOST" "$LAB_VM_IP"
  printf 'silent\nshow-error\n'
  printf 'header = "Accept: application/json"\n'
  printf 'header = "Content-Type: application/json"\n'
  printf 'write-out = "\\nHTTP %%{http_code}\\n"\n'
} > "$cfg"
args=(--config "$cfg" -X "$method")
[ -n "$body" ] && args+=(--data-binary "@$body")
curl "${args[@]}" "https://$LAB_PRISM_HOST:8443$path" | awk 'prev!="" {print prev} {prev=$0} END {print prev > "/dev/stderr"}'
