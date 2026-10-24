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

# I3: the lab IdP is selected by name (LAB_SAML_IDP, default the lab
# VM's), never "the first one".
if grep -q 'select(.name==$n)' lab/projects/lib.sh && grep -q 'LAB_SAML_IDP:-portico-lab-keycloak' lab/projects/lib.sh; then ok "lib.sh selects the lab IdP by name"; else ko "lib.sh picks the first SAML IdP"; fi

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
# Milestone 5 Task 11: the tighter sandbox, and the workflows' supply chain.
missing=""
for d in UMask=0077 'RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK' SystemCallFilter=@system-service SystemCallArchitectures=native RestrictNamespaces=yes ProtectHostname=yes ProtectClock=yes ProtectProc=invisible ProtectKernelLogs=yes MemoryDenyWriteExecute=yes; do
  grep -qxF -- "$d" deploy/portico.service || missing="$missing $d"
done
if [ -z "$missing" ]; then ok "portico.service carries the Milestone 5 sandbox directives"; else ko "portico.service lacks:$missing"; fi
unpinned=$(grep -h 'uses:' .github/workflows/*.yml | grep -vE '@[0-9a-f]{40}( |$)')
if [ -z "$unpinned" ]; then ok "every GitHub action is pinned by commit"; else ko "actions pinned by tag: $unpinned"; fi
if [ "$(grep -c 'contents: write' .github/workflows/image.yml)" = 1 ] && grep -A1 '^permissions:' .github/workflows/image.yml | grep -q 'contents: read' && [ "$(grep -n 'contents: write' .github/workflows/image.yml | cut -d: -f1)" -gt "$(grep -n '^  release:' .github/workflows/image.yml | cut -d: -f1)" ]; then ok "image.yml gives contents: write to the release job only"; else ko "image.yml token scope"; fi
# M1-4: Keycloak publishes backchannel endpoints dynamically (OIDC over loopback).
if grep -q 'KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true' lab/vm/keycloak.container; then ok "keycloak.container enables dynamic backchannel"; else ko "keycloak.container lacks KC_HOSTNAME_BACKCHANNEL_DYNAMIC"; fi

# M1-5: the Keycloak admin helper uses the VM loopback over SSH, never the public name (Portico refuses /realms/master).
if grep -q 'realms/master' lab/saml/kc.sh && ! grep -q 'LAB_ID_HOST' lab/saml/kc.sh && grep -q '127.0.0.1:8080' lab/saml/kc.sh; then ok "kc.sh talks to Keycloak on the VM loopback"; else ko "kc.sh still uses the public identity name"; fi

# Milestone 2a Task 6: tenant API deployment.
# M2-1: the break-glass password reaches the binary on stdin, never on a command line.
if grep -q 'admin set-password -config /etc/portico/portico.yaml < ' lab/portico/deploy.sh && ! grep -qE 'set-password.*\$\(cat' lab/portico/deploy.sh; then ok "deploy.sh passes the admin password on stdin"; else ko "deploy.sh puts the admin password on a command line"; fi
# M2-2: only the key part of the service-account file is staged.
if grep -q 'cut -d= -f2- "\$S/portico-sa-key"' lab/portico/deploy.sh; then ok "deploy.sh stages the API key only"; else ko "deploy.sh stages the whole portico-sa-key file"; fi
# M2-3: the lab project prefix keeps the portico-lab-* safety rule.
if grep -q '^  project_prefix: portico-lab' lab/portico/portico.yaml; then ok "portico.yaml keeps the portico-lab prefix"; else ko "portico.yaml prefix is not portico-lab"; fi
# M2-4: the Bearer/v3 probe keeps bob's credentials in curl config files.
if ! grep -nE -- '-u "portico-lab-bob|Bearer \$' lab/auth/probe-bearer-and-v3-users.sh | grep -q .; then ok "probe-bearer-and-v3-users.sh keeps credentials off the command line"; else ko "probe-bearer-and-v3-users.sh puts credentials on the command line"; fi

# Milestone 2d Task 6: settings in the store.
# M2d-1: the store YAML names no secret file and no runtime section.
if [ -s lab/portico/portico.store.yaml ] && ! grep -qE 'api_key|client_secret|admin_client_secret|prism_host|saml_idp_name|issuer|project_prefix' lab/portico/portico.store.yaml; then ok "portico.store.yaml holds process settings only"; else ko "portico.store.yaml names a secret file or a runtime section"; fi
# M2d-2: deploy.sh imports once, installs the store YAML as the live one and removes the three secret files.
if grep -q 'portico settings import -config /etc/portico/portico.import.yaml' lab/portico/deploy.sh && grep -q '"$st/portico.store.yaml" /etc/portico/portico.yaml' lab/portico/deploy.sh && grep -q 'rm -f /etc/portico/oidc-client-secret /etc/portico/keycloak-admin-secret /etc/portico/prism-api-key' lab/portico/deploy.sh; then ok "deploy.sh imports then removes the secret files"; else ko "deploy.sh does not import into the store or leaves secret files"; fi
# M2d-3: the unit restarts after the exit 0 of "Apply and restart".
if grep -q '^Restart=always' deploy/portico.service && grep -q '^RestartSec=2' deploy/portico.service; then ok "portico.service restarts always"; else ko "portico.service lacks Restart=always"; fi

# Milestone 2f Task 9: an appliance recovers by itself.
# M2f-1: no start limit, so a store that cannot open keeps the unit retrying instead of leaving it failed.
if grep -q '^StartLimitIntervalSec=0' deploy/portico.service; then ok "portico.service has no start limit"; else ko "portico.service still has the default start limit"; fi

# Milestone 2e Task 6: fresh-appliance bootstrap.
# M2e-1: the bootstrap acceptance builds credential bodies in private files, never on a command line.
if grep -q 'umask 077' lab/portico/bootstrap-acceptance.sh && grep -q -- '--rawfile' lab/portico/bootstrap-acceptance.sh && ! grep -nE 'password=\$|--arg (p|pw|password) "\$\(cat' lab/portico/bootstrap-acceptance.sh | grep -q .; then ok "bootstrap-acceptance.sh keeps credentials in files"; else ko "bootstrap-acceptance.sh exposes a credential"; fi
# M2e-2: the reset refuses to run without --yes.
out=$(LAB_VM_IP=x PC_URL=https://invalid.test PC_USER=x PC_PASS=x bash lab/portico/reset-for-bootstrap.sh 2>&1); rc=$?
if [ "$rc" -eq 2 ] && grep -q -- '--yes' <<<"$out"; then ok "reset-for-bootstrap.sh needs --yes"; else ko "reset-for-bootstrap.sh ran without --yes (rc=$rc)"; fi
# M2e-3: deploy.sh --fresh skips the import and needs no secret file.
if grep -q 'fresh' lab/portico/deploy.sh && grep -q 'import skipped' lab/portico/deploy.sh; then ok "deploy.sh has a --fresh path"; else ko "deploy.sh lacks --fresh"; fi
# M2e-4: the lab admin client has the client-management roles the SAML bootstrap needs.
if grep -q 'manage-clients' lab/saml/keycloak-realm.sh && grep -q 'manage-clients' deploy/portico.yaml.example; then ok "admin-client roles include manage-clients"; else ko "manage-clients missing from the lab script or the example"; fi

# Milestone 3: the installer renders the appliance files without root (Task 2).
# A plain render is the image build: no secret and no TLS pair in the image,
# the first-boot unit generates them; --render-secrets is the first boot.
d=$(mktemp -d); bin=$(mktemp); chmod +x "$bin"
if bash deploy/install.sh --render-only "$d" --binary "$bin" >/dev/null 2>&1; then ok "install.sh --render-only runs without root"; else ko "install.sh --render-only failed"; fi
for f in etc/portico/portico.yaml etc/containers/systemd/keycloak.container etc/containers/systemd/postgres.container etc/containers/systemd/portico.network etc/systemd/system/portico.service etc/systemd/system/getty@tty1.service.d/override.conf etc/systemd/system/cloudflared.service etc/systemd/system/portico-firstboot.service etc/systemd/system/keycloak-hostname.path etc/firewalld/services/portico.xml etc/ssh/sshd_config.d/00-portico.conf etc/sysctl.d/60-portico.conf usr/local/bin/portico usr/local/sbin/portico-install; do
  if [ -e "$d/$f" ]; then ok "install.sh renders $f"; else ko "install.sh does not render $f"; fi
done
if [ ! -e "$d/etc/portico/keycloak.env" ] && [ ! -e "$d/etc/portico/tls/internal.key" ]; then ok "the image build renders no secret and no TLS pair"; else ko "a secret or a TLS pair would ship in the image"; fi
if [ -e "$d/var/lib/portico/keycloak.hostname.env" ] && grep -q '^EnvironmentFile=/var/lib/portico/keycloak.hostname.env' "$d/etc/containers/systemd/keycloak.container" && ! grep -q 'EnvironmentFile=-' "$d/etc/containers/systemd/keycloak.container"; then ok "keycloak.hostname.env exists from the install and the quadlet loads it without the dash (measured: podman takes the dash literally)"; else ko "keycloak hostname file or quadlet"; fi
if grep -q 'KC_HOSTNAME_STRICT=false' "$d/etc/containers/systemd/keycloak.container" && ! grep -qi 'lab' "$d/etc/containers/systemd/keycloak.container"; then ok "keycloak.container learns its name from the request, no lab name"; else ko "keycloak.container hostname"; fi
mode() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
bash deploy/install.sh --render-only "$d" --render-secrets --binary "$bin" >/dev/null 2>&1
if grep -q 'KC_BOOTSTRAP_ADMIN_PASSWORD=' "$d/etc/portico/keycloak.env" && [ "$(mode "$d/etc/portico/keycloak.env")" = 600 ]; then ok "first boot: keycloak.env generated, mode 0600"; else ko "keycloak.env"; fi
if [ "$(mode "$d/etc/portico/tls/internal.key")" = 640 ] && openssl x509 -in "$d/etc/portico/tls/internal.crt" -noout >/dev/null 2>&1 && openssl x509 -in "$d/etc/portico/tls/public.crt" -noout >/dev/null 2>&1; then ok "first boot: internal and placeholder public TLS pairs generated"; else ko "TLS pairs"; fi
first=$(grep KC_BOOTSTRAP_ADMIN_PASSWORD "$d/etc/portico/keycloak.env"); firstkey=$(cat "$d/etc/portico/tls/internal.key")
bash deploy/install.sh --render-only "$d" --render-secrets --binary "$bin" >/dev/null 2>&1
if [ "$(grep KC_BOOTSTRAP_ADMIN_PASSWORD "$d/etc/portico/keycloak.env")" = "$first" ] && [ "$(cat "$d/etc/portico/tls/internal.key")" = "$firstkey" ]; then ok "install.sh keeps the secrets on a second run"; else ko "install.sh regenerated a secret"; fi
# Milestone 5 Task 9: the installer runs from the unpacked release (its quadlets and units are beside it),
# installs itself from BASH_SOURCE, and restarts PostgreSQL and Keycloak only when their quadlets changed.
alone=$(mktemp -d); cp deploy/install.sh "$alone/install.sh"
out=$(bash "$alone/install.sh" --render-only "$d" --binary "$bin" 2>&1); rc=$?
if [ $rc -ne 0 ] && printf '%s' "$out" | grep -q 'run it from the unpacked release'; then ok "install.sh copied alone refuses and names the missing release tree"; else ko "install.sh copied alone: rc=$rc $out"; fi
if cmp -s deploy/install.sh "$d/usr/local/sbin/portico-install"; then ok "install.sh installs itself as portico-install"; else ko "portico-install differs from install.sh"; fi
out=$(bash deploy/install.sh --render-only "$d" --render-secrets --binary "$bin" 2>&1)
if ! printf '%s' "$out" | grep -q 'would restart'; then ok "install.sh: an unchanged second run restarts no container"; else ko "install.sh would restart unchanged containers: $out"; fi
echo "# edited" >> "$d/etc/containers/systemd/keycloak.container"
out=$(bash deploy/install.sh --render-only "$d" --render-secrets --binary "$bin" 2>&1)
if printf '%s' "$out" | grep -q 'would restart: postgres keycloak'; then ok "install.sh restarts PostgreSQL and Keycloak when a quadlet changed"; else ko "install.sh: changed quadlet not restarted: $out"; fi
# Review M5: the installed copy (/usr/local/sbin/portico-install, alone in its directory) must still run --first-boot.
fb=$(mktemp -d); cp deploy/install.sh "$alone/portico-install"
out=$(bash "$alone/portico-install" --first-boot --render-only "$fb" --render-secrets 2>&1); rc=$?
if [ $rc -eq 0 ] && printf '%s' "$out" | grep -q 'first boot done' && [ -s "$fb/etc/portico/keycloak.env" ]; then ok "portico-install --first-boot runs from a bare directory"; else ko "portico-install --first-boot refused: rc=$rc $out"; fi
if [ -e "$d/etc/systemd/system/sshd.service.d/portico.conf" ] && grep -q 'After=network-online.target' "$d/etc/systemd/system/sshd.service.d/portico.conf"; then ok "install.sh orders sshd after network-online (a ListenAddress needs its address)"; else ko "no sshd unit drop-in rendered"; fi
if grep -q 'ConditionPathExists=!/etc/portico/keycloak.env' "$d/etc/systemd/system/portico-firstboot.service" && grep -q 'portico-install --first-boot' "$d/etc/systemd/system/portico-firstboot.service"; then ok "first-boot unit runs once, before the services"; else ko "first-boot unit"; fi
if grep -q 'rm -f /etc/portico/keycloak.env /etc/portico/tls/\*' image/scripts/cleanup.sh; then ok "the image cleanup removes every secret"; else ko "cleanup leaves secrets in the image"; fi
# Milestone 5 Task 10: the build fails on an OpenSCAP rule that fails and is not an expected failure.
fx=$(mktemp -d)
cat > "$fx/results.xml" <<'XML'
<TestResult>
  <rule-result idref="xccdf_org.ssgproject.content_rule_listed_one" time="t" severity="medium" weight="1.0">
    <result>fail</result>
  </rule-result>
  <rule-result idref="xccdf_org.ssgproject.content_rule_fine" time="t" severity="medium" weight="1.0">
    <result>pass</result>
  </rule-result>
  <rule-result idref="xccdf_org.ssgproject.content_rule_new_one" time="t" severity="medium" weight="1.0">
    <result>fail</result>
  </rule-result>
</TestResult>
XML
printf '# a comment\n\nxccdf_org.ssgproject.content_rule_listed_one  # why\nxccdf_org.ssgproject.content_rule_fine\n' > "$fx/expected.txt"
out=$(bash -c 'source image/scripts/harden.sh && oscap_regressions "$1" "$2"' _ "$fx/results.xml" "$fx/expected.txt" 2>&1); rc=$?
if [ $rc -eq 1 ] && printf '%s' "$out" | grep -q 'harden: new failing rule: xccdf_org.ssgproject.content_rule_new_one' && ! printf '%s' "$out" | grep -q 'new failing rule: xccdf_org.ssgproject.content_rule_listed_one' && printf '%s' "$out" | grep -q 'harden: expected failure now passes: xccdf_org.ssgproject.content_rule_fine'; then ok "harden.sh names the new failing rule and the expected failure that passes, and fails"; else ko "harden.sh regression check: rc=$rc $out"; fi
printf 'xccdf_org.ssgproject.content_rule_listed_one\nxccdf_org.ssgproject.content_rule_new_one\n' > "$fx/expected.txt"
if bash -c 'source image/scripts/harden.sh && oscap_regressions "$1" "$2"' _ "$fx/results.xml" "$fx/expected.txt" >/dev/null 2>&1; then ok "harden.sh passes when every failure is expected"; else ko "harden.sh fails on expected failures only"; fi
if [ -f image/oscap/expected-failures.txt ]; then ok "image/oscap/expected-failures.txt exists"; else ko "no expected-failures.txt"; fi
# Review M5: a scan that did not run is not a clean scan.
: > "$fx/empty.xml"
if ! bash -c 'source image/scripts/harden.sh && oscap_results_ok "$1" 0' _ "$fx/empty.xml" >/dev/null 2>&1 && ! bash -c 'source image/scripts/harden.sh && oscap_results_ok "$1" 0' _ "$fx/missing.xml" >/dev/null 2>&1 && ! bash -c 'source image/scripts/harden.sh && oscap_results_ok "$1" 1' _ "$fx/results.xml" >/dev/null 2>&1 && bash -c 'source image/scripts/harden.sh && oscap_results_ok "$1" 2' _ "$fx/results.xml" >/dev/null 2>&1; then ok "harden.sh refuses an empty, missing or failed evaluation and accepts exit 2"; else ko "harden.sh accepts a scan that did not run"; fi
# An existing host keeps SSH open to its current session: the policy needs a wheel user with a key (review of Milestone 3).
if grep -q 'ssh_policy_safe' deploy/install.sh && grep -q 'if \[ \$START -eq 0 \] || ssh_policy_safe; then' deploy/install.sh; then ok "install.sh installs the SSH policy on an existing host only when a wheel user has a key"; else ko "install.sh may lock SSH out"; fi
# The first boot must work offline: its block comes before any dnf in install.sh (measured on NTNX-01).
if [ "$(grep -n 'FIRSTBOOT -eq 1 \]; then' deploy/install.sh | head -1 | cut -d: -f1)" -lt "$(grep -n 'dnf -y -q install' deploy/install.sh | head -1 | cut -d: -f1)" ]; then ok "install.sh --first-boot runs before any package install"; else ko "install.sh --first-boot would run dnf"; fi
# Milestone 3 Task 5: the appliance lab scripts keep the Prism password off the command line and off the tfvars file.
if grep -q 'TF_VAR_nutanix_password="$PC_PASS"' lab/appliance/deploy.sh && ! grep -q '^nutanix_password' lab/appliance/deploy.sh; then ok "appliance deploy.sh passes the password in the environment only"; else ko "appliance deploy.sh leaks the password"; fi
out=$(env -i PATH="$PATH" HOME="$HOME" bash lab/appliance/deploy.sh 2>&1); rc=$?
if [ "$rc" -ne 0 ] && grep -q 'source lab/env.sh' <<<"$out"; then ok "appliance deploy.sh needs lab/env.sh"; else ko "appliance deploy.sh ran without env (rc=$rc)"; fi
# 443 and 8443 only: the pass-through binds the loopback until an administrator moves it to an internal interface (design 4.2).
if grep -q 'port="443"' "$d/etc/firewalld/services/portico.xml" && grep -q 'port="8443"' "$d/etc/firewalld/services/portico.xml" && ! grep -q 'port="9440"' "$d/etc/firewalld/services/portico.xml" && ! grep -q 'port="8080"' "$d/etc/firewalld/services/portico.xml"; then ok "firewalld service: 443 and 8443 only"; else ko "firewalld service ports"; fi
if grep -q 'PermitRootLogin no' "$d/etc/ssh/sshd_config.d/00-portico.conf" && grep -q 'PasswordAuthentication no' "$d/etc/ssh/sshd_config.d/00-portico.conf"; then ok "sshd: key only, no root"; else ko "sshd policy"; fi
if grep -q 'ExecStart=-/usr/local/bin/portico console' "$d/etc/systemd/system/getty@tty1.service.d/override.conf"; then ok "tty1 runs the console"; else ko "tty1 override"; fi
if grep -q 'passthrough_listen: "127.0.0.1:9440"' "$d/etc/portico/portico.yaml" && grep -q 'admin_listen: "0.0.0.0:8443"' "$d/etc/portico/portico.yaml" && ! grep -q 'prism_central:' "$d/etc/portico/portico.yaml"; then ok "appliance YAML: process settings only, pass-through on the loopback"; else ko "appliance YAML"; fi
if grep -q -- '--token-file /etc/cloudflared/token' "$d/etc/systemd/system/cloudflared.service" 2>/dev/null || grep -q 'token' "$d/etc/systemd/system/cloudflared.service"; then ok "cloudflared unit reads the token file"; else ko "cloudflared unit"; fi
if ! grep -rn '10\.23\.210\|lab\.portico\.test\|portico-lab' deploy/ | grep -q .; then ok "deploy/ names no lab host"; else ko "deploy/ names the lab"; fi
rm -rf "$d"


# Milestone 4 (write-up 21): the tenant acceptance writes and removes only
# its own test user's password file, never the appliance's carol-tmp-pw of
# tenant acme; a deletion check never reads a failed lookup as "absent";
# the local-login probe takes the appliance's realm and loopback
# pass-through.
if grep -nE '\$S/carol' lab/tenants/acceptance.sh | grep -q .; then ko "tenants/acceptance.sh touches \$S/carol-*"; else ok "tenants/acceptance.sh keeps to its own password file"; fi
if grep -q '^gone()' lab/tenants/acceptance.sh && ! grep -q 'absent || echo present)" absent' lab/tenants/acceptance.sh; then ok "tenants/acceptance.sh: a failed lookup is not absent"; else ko "tenants/acceptance.sh reads a failed lookup as absent"; fi
if grep -q 'realm="${LAB_REALM:-portico-lab}"' lab/auth/probe-local-login.sh && grep -q 'LAB_PASSTHROUGH:-exposed' lab/auth/probe-local-login.sh && ! grep -q 'realms/portico-lab/' lab/auth/probe-local-login.sh; then ok "probe-local-login.sh: realm and pass-through from LAB_*"; else ko "probe-local-login.sh hardcodes the lab VM"; fi

# Sites usability measurements (write-ups 24 and 25): each script prints its usage
# without the lab environment.
for sc in project-contents admin-rights container-kinds connections; do
  out=$(env -i PATH="$PATH" HOME="$HOME" bash "lab/sites/$sc.sh" --help 2>&1); rc=$?
  if [ "$rc" -eq 0 ] && grep -q "lab/sites/$sc.sh" <<<"$out"; then ok "$sc.sh --help"; else ko "$sc.sh --help (rc=$rc)"; fi
done

# Branding (write-up 23): the demo script prints its usage, refuses
# exclusive logo options before any call, and knows the new fields like the
# bootstrap and tab icon scripts.
out=$(env -i PATH="$PATH" HOME="$HOME" bash lab/portico/demo-branding.sh --help 2>&1); rc=$?
if [ "$rc" -eq 0 ] && grep -q -- '--restore FILE' <<<"$out"; then ok "demo-branding.sh --help"; else ko "demo-branding.sh --help (rc=$rc)"; fi
out=$(env -i PATH="$PATH" HOME="$HOME" bash lab/portico/demo-branding.sh --arch --no-logo 2>&1); rc=$?
if [ "$rc" -eq 2 ] && grep -q 'exclusive' <<<"$out"; then ok "demo-branding.sh refuses two logo choices"; else ko "demo-branding.sh accepted two logo choices (rc=$rc)"; fi
if grep -q 'text_color:"#ffffff"' lab/portico/bootstrap-acceptance.sh && grep -q 'portico_logo,logo_type' lab/portico/tab-icons.sh; then ok "bootstrap and tab icon scripts know the new branding fields"; else ko "a lab script ignores the new branding fields"; fi

if shellcheck -x lab/*.sh lab/*/*.sh deploy/install.sh; then ok "shellcheck"; else ko "shellcheck"; fi
if [ $fail -eq 0 ]; then echo PASS; else echo FAIL; exit 1; fi
