#!/usr/bin/env bash
# Lab environment for Portico. Source it: `source lab/env.sh`.
# Lab only. Reads the Prism password from ~/portico-secrets/prism-pw and
# never prints it. Safe under `set -u` and `set -x`.

PORTICO_SECRETS="${PORTICO_SECRETS:-$HOME/portico-secrets}"

if [ ! -r "$PORTICO_SECRETS/prism-pw" ]; then
  echo "lab/env.sh: secrets file not found or not readable: $PORTICO_SECRETS/prism-pw" >&2
  # shellcheck disable=SC2317  # `exit` runs only when the file is executed, not sourced
  return 1 2>/dev/null || exit 1
fi

export PC_URL="${PC_URL:-https://10.23.210.175:9440}"
export PE_URL="${PE_URL:-https://10.23.210.170:9440}"
export PC_USER="${PC_USER:-admin}"
# Read the password with tracing off, then restore the caller's setting.
case $- in *x*) _portico_xtrace=1 ;; *) _portico_xtrace= ;; esac
{ set +x; } 2>/dev/null
PC_PASS="$(tr -d '\r\n' < "$PORTICO_SECRETS/prism-pw")"
export PC_PASS
[ -n "$_portico_xtrace" ] && set -x
unset _portico_xtrace

export LAB_VM_IP="${LAB_VM_IP:-10.23.210.190}"
export LAB_PRISM_HOST="${LAB_PRISM_HOST:-prism.lab.portico.test}"
export LAB_ID_HOST="${LAB_ID_HOST:-id.lab.portico.test}"
