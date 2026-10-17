#!/usr/bin/env bash
# Call the Prism Central API. Lab only.
#   lab/pc.sh METHOD PATH [JSON_FILE]
# Prints the response body on stdout and "HTTP <code>" on stderr.
# Requires `source lab/env.sh` first. The password goes to curl through a
# temporary config file (mode 0600), never on the command line.
# For POST/PUT/PATCH on a v4 path it adds NTNX-Request-Id; if IF_MATCH is
# set it adds If-Match (v4 updates need the ETag). If PC_HEADERS_FILE is set,
# response headers are written there.
set -euo pipefail

method="${1:?usage: pc.sh METHOD PATH [JSON_FILE]}"
path="${2:?usage: pc.sh METHOD PATH [JSON_FILE]}"
body="${3:-}"
: "${PC_URL:?source lab/env.sh first}" "${PC_USER:?}" "${PC_PASS:?}"

umask 077
cfg="$(mktemp)"
trap 'rm -f "$cfg"' EXIT
{
  printf 'user = "%s:%s"\n' "$PC_USER" "$PC_PASS"
  printf 'insecure\nsilent\nshow-error\n'
  printf 'header = "Accept: application/json"\n'
  printf 'header = "Content-Type: application/json"\n'
  printf 'write-out = "\\nHTTP %%{http_code}\\n"\n'
  # Optional: response headers to a file (v4 actions need the ETag).
  [ -n "${PC_HEADERS_FILE:-}" ] && printf 'dump-header = "%s"\n' "$PC_HEADERS_FILE"
} > "$cfg"

args=(--config "$cfg" -X "$method")
case "$method" in
  POST|PUT|PATCH|DELETE)
    if [[ "$path" == /api/*/v4* ]]; then
      args+=(-H "NTNX-Request-Id: $(uuidgen | tr '[:upper:]' '[:lower:]')")
      [ -n "${IF_MATCH:-}" ] && args+=(-H "If-Match: $IF_MATCH")
    fi
    [ -n "$body" ] && args+=(--data-binary "@$body")
    ;;
esac

# Body to stdout, the "HTTP <code>" trailer to stderr.
curl "${args[@]}" "$PC_URL$path" | awk 'prev!="" {print prev} {prev=$0} END {print prev > "/dev/stderr"}'
