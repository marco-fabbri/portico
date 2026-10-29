#!/usr/bin/env bash
# Lab environment for Portico. Source it: `source lab/env.sh`.
# Lab only. Reads the Prism password from ~/portico-secrets and never
# prints it: prism-pw for NTNX-01, nyc1-pw when PC_URL is NYC1's Prism
# Central (the two sites have different admin passwords). Safe under
# `set -u` and `set -x`.

PORTICO_SECRETS="${PORTICO_SECRETS:-$HOME/portico-secrets}"
export PC_URL="${PC_URL:-https://10.23.210.175:9440}"
case "$PC_URL" in
  https://10.23.210.185:9440*) _portico_pw=nyc1-pw ;;
  *) _portico_pw=prism-pw ;;
esac

if [ ! -r "$PORTICO_SECRETS/$_portico_pw" ]; then
  echo "lab/env.sh: secrets file not found or not readable: $PORTICO_SECRETS/$_portico_pw" >&2
  unset _portico_pw
  # shellcheck disable=SC2317  # `exit` runs only when the file is executed, not sourced
  return 1 2>/dev/null || exit 1
fi

export PE_URL="${PE_URL:-https://10.23.210.170:9440}"
export PC_USER="${PC_USER:-admin}"
# Read the password with tracing off, then restore the caller's setting.
case $- in *x*) _portico_xtrace=1 ;; *) _portico_xtrace= ;; esac
{ set +x; } 2>/dev/null
PC_PASS="$(tr -d '\r\n' < "$PORTICO_SECRETS/$_portico_pw")"
export PC_PASS
[ -n "$_portico_xtrace" ] && set -x
unset _portico_xtrace _portico_pw

# The appliance is the Portico of the lab (write-ups 21 to 27): its
# address, SSH user, portal names, realm, identity provider name, project
# prefix and loopback pass-through are the defaults. The VM of Milestones
# 0 to 2 is retired; its scripts under lab/vm still run with the LAB_*
# values of write-up 15 set by hand. PORTICO_SECRETS holds the
# appliance's files (portico-admin-pw, keycloak-admin-pw, probe-*-pw).
export LAB_VM_IP="${LAB_VM_IP:-10.23.210.191}"
export LAB_SSH_USER="${LAB_SSH_USER:-admin}"
export LAB_PRISM_HOST="${LAB_PRISM_HOST:-msp.portico.test}"
export LAB_ID_HOST="${LAB_ID_HOST:-id.portico.test}"
export LAB_REALM="${LAB_REALM:-portico}"
export LAB_SAML_IDP="${LAB_SAML_IDP:-portico-keycloak}"
export LAB_PROJECT_PREFIX="${LAB_PROJECT_PREFIX:-portico}"
export LAB_SERVICE_ACCOUNT="${LAB_SERVICE_ACCOUNT:-portico}"
export LAB_PASSTHROUGH="${LAB_PASSTHROUGH:-loopback}"
