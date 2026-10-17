#!/usr/bin/env bash
# shellcheck disable=SC2016  # literal $ in jq/grep patterns is intended
# Static and offline checks on the lab scripts (findings of the Milestone 0 review).
set -u
cd "$(dirname "$0")/.." || exit 1
fail=0; ok() { echo "ok   $1"; }; ko() { echo "FAIL $1"; fail=1; }

# C1: nginx must never log the Authorization header value.
if grep -q 'auth="\$http_authorization"' lab/vm/nginx.conf; then ko "nginx.conf logs the Authorization value"; else ok "nginx.conf does not log the Authorization value"; fi

# I1: no credential on a curl command line in the probe.
if grep -nE -- '-u "admin:|j_password=\$PC_PASS|X-ntnx-api-key: \$' lab/auth/probe-local-login.sh | grep -q .; then ko "probe-local-login.sh puts credentials on the command line"; else ok "probe-local-login.sh keeps credentials in curl config files"; fi

# I2: project scripts refuse names outside portico-lab-* before any API call.
for s in create bind-group delete; do
  out=$(PC_URL=https://invalid.test PC_USER=x PC_PASS=x LAB_PRISM_HOST=x LAB_ID_HOST=x LAB_VM_IP=x bash "lab/projects/$s.sh" not-a-lab-project tenant-x 2>&1); rc=$?
  if [ "$rc" -eq 2 ] && grep -q 'refusing' <<<"$out"; then ok "$s.sh refuses non-lab project names"; else ko "$s.sh accepted a non-lab name (rc=$rc): $out"; fi
done

# I3: the lab IdP is selected by name, never "the first one".
if grep -q 'select(.name=="portico-lab-keycloak")' lab/projects/lib.sh; then ok "lib.sh selects the lab IdP by name"; else ko "lib.sh picks the first SAML IdP"; fi

# I4: create-vm.sh checks the IP (ping + Prism Central NIC list) before creating.
if grep -q 'learnedIpAddresses' lab/vm/create-vm.sh && grep -q 'ping -c' lab/vm/create-vm.sh; then ok "create-vm.sh checks the IP before creating"; else ko "create-vm.sh does not check the IP"; fi

# I5: create-service-account.sh guards the policy lookup and the POST codes.
if grep -q 'no policy named' lab/auth/create-service-account.sh && grep -q '^post()' lab/auth/create-service-account.sh; then ok "create-service-account.sh guards policy lookup and POST codes"; else ko "create-service-account.sh lacks guards"; fi

if shellcheck -x lab/*.sh lab/*/*.sh; then ok "shellcheck"; else ko "shellcheck"; fi
if [ $fail -eq 0 ]; then echo PASS; else echo FAIL; exit 1; fi
