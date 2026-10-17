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

# Milestone 0 review minors, fixed in Milestone 1 Task 0.
# M0-min-1: one definition of wait_task and etag_of, in lab/lib.sh.
for f in wait_task etag_of; do
  n=$(grep -l "^$f()" lab/*.sh lab/*/*.sh | wc -l | tr -d ' ')
  if [ "$n" = 1 ] && grep -q "^$f()" lab/lib.sh 2>/dev/null; then ok "$f defined once in lab/lib.sh"; else ko "$f defined $n times"; fi
done
# M0-min-2: wait_task gives up.
if grep -q 'did not finish' lab/lib.sh 2>/dev/null; then ok "wait_task has a timeout"; else ko "wait_task loops forever"; fi
# M0-min-3: no fixed /tmp paths, on the Mac or on the VM.
if grep -n '/tmp/' lab/*.sh lab/*/*.sh | grep -v '^lab/test_scripts.sh' | grep -q .; then ko "fixed /tmp path in lab scripts"; else ok "no fixed /tmp path"; fi
# M0-min-4: env.sh restores xtrace when the caller had it on.
d=$(mktemp -d); printf 'x\n' > "$d/prism-pw"; chmod 600 "$d/prism-pw"
out=$(PORTICO_SECRETS="$d" bash -c 'set -x; source lab/env.sh 2>/dev/null; case $- in *x*) echo on;; *) echo off;; esac' 2>/dev/null); rm -rf "$d"
if [ "$out" = on ]; then ok "env.sh restores xtrace"; else ko "env.sh leaves xtrace off"; fi
# M0-min-5: create-vm.sh names a missing cluster or subnet and does not pass the SSH key through sed.
if grep -q 'no AOS cluster' lab/vm/create-vm.sh && grep -q 'no subnet named' lab/vm/create-vm.sh && ! grep -q 'sed .*__SSH_KEY__' lab/vm/create-vm.sh; then ok "create-vm.sh fails loudly and renders the template safely"; else ko "create-vm.sh silent exit or sed on the SSH key"; fi
# M0-min-5b: the cloud-init template keeps `|`, `&` and `\` of the SSH key literal (bash 5.2+ patsub_replacement).
out=$(bash -c 'source lab/vm/lib.sh; render_cloud_init "ssh-ed25519 AAAA marco|test&co\\x" 10.0.0.1 10.0.0.254' 2>&1)
if grep -Fq -- '- ssh-ed25519 AAAA marco|test&co\x' <<<"$out" && grep -Fq 'address1=10.0.0.1/24,10.0.0.254' <<<"$out"; then ok "render_cloud_init keeps the key literal"; else ko "render_cloud_init altered the key"; fi
# M0-min-6: the authorization policy payload promised by the Milestone 0 plan exists and is sanitised.
if [ -s docs/lab/payloads/authorization-policy-update.json ] && ! grep -qE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' docs/lab/payloads/authorization-policy-update.json; then ok "authorization-policy-update.json present and sanitised"; else ko "authorization-policy-update.json missing or carries UUIDs"; fi

# Milestone 1 Task 8: deployment of the Go proxy to the lab VM.
# M1-1: the lab config file carries no secret value, only file paths.
if [ -s lab/portico/portico.yaml ] && ! grep -nE 'secret:|password|apiKey' lab/portico/portico.yaml | grep -vq 'client_secret_file'; then ok "portico.yaml references secrets by path only"; else ko "portico.yaml missing or holds a secret value"; fi
# M1-2: deploy.sh refuses to run without the lab environment.
out=$(env -i PATH="$PATH" HOME="$HOME" bash lab/portico/deploy.sh 2>&1); rc=$?
if [ "$rc" -ne 0 ] && grep -q 'source lab/env.sh' <<<"$out"; then ok "deploy.sh needs lab/env.sh"; else ko "deploy.sh ran without env (rc=$rc)"; fi
# M1-3: the unit runs unprivileged and binds 443 through a capability.
if grep -q '^User=portico' deploy/portico.service 2>/dev/null && grep -q 'AmbientCapabilities=CAP_NET_BIND_SERVICE' deploy/portico.service && grep -q 'ProtectSystem=strict' deploy/portico.service; then ok "portico.service is unprivileged and hardened"; else ko "portico.service lacks hardening"; fi
# M1-4: Keycloak publishes backchannel endpoints dynamically (OIDC over loopback).
if grep -q 'KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true' lab/vm/keycloak.container; then ok "keycloak.container enables dynamic backchannel"; else ko "keycloak.container lacks KC_HOSTNAME_BACKCHANNEL_DYNAMIC"; fi

if shellcheck -x lab/*.sh lab/*/*.sh; then ok "shellcheck"; else ko "shellcheck"; fi
if [ $fail -eq 0 ]; then echo PASS; else echo FAIL; exit 1; fi
