#!/usr/bin/env bash
# shellcheck disable=SC2016  # literal $ in jq programs and v4 paths is intended
# The demo on a set-up appliance: two tenants, one user each with a
# password that is not temporary, and small powered-on VMs in their
# projects. Idempotent: what exists is kept. Lab only.
#   source lab/env.sh && lab/portico/demo-tenants.sh
# Tenants: acme (ACME Cloud) on both sites, globex (Globex) on the first
# only. Users carol@acme.example and dave@globex.example; their passwords
# are generated once into $PORTICO_SECRETS/<slug>-user-pw (0600) and never
# printed. Users are created through Portico's API (the real path, which
# gives a temporary password); the permanent one is set with a Keycloak
# master administrator created for this run with the recovery command of
# docs/operating.md and deleted at the end, as the realm job leaves none.
# VMs: Rocky 9 GenericCloud, 1 vCPU, 1 GiB, no NIC, created as each site's
# administrator (passwords: ~/portico-secrets/prism-pw for AZ-IT-MI1,
# nyc1-pw for AZ-US-NYC1) with the tenant's project, cluster and storage
# container.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${PC_USER:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
REALM="${LAB_REALM:-portico}"
IMAGE="${LAB_DEMO_IMAGE:-rocky-9-genericcloud}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
# shellcheck source=lab/vm/lib.sh
source lab/vm/lib.sh
umask 077; tmp=$(mktemp -d)
read -r -a extra <<<"${LAB_SSH_OPTS:-}"
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=8 -o LogLevel=ERROR "${extra[@]}" "${LAB_SSH_USER:-admin}@$LAB_VM_IP")
fail=0
api() { local code; code=$(lab/tenants/api.sh "$@" 2>&1 >"$tmp/out" | tail -1); echo "${code#HTTP }"; }
field() { jq -r "$1" "$tmp/out" 2>/dev/null; }
say() { printf '%-4s %s\n' "$1" "$2"; [ "$1" = FAIL ] && fail=1; return 0; }

# The sites as the appliance has them: id, Prism Central URL, cluster and
# storage container of the placement defaults.
api GET /api/v1/sites >/dev/null
jq -r '.sites[] | "\(.id) \(.url) \(.cluster_ext_id) \(.storage_container_ext_id)"' "$tmp/out" > "$tmp/sites"
[ -s "$tmp/sites" ] || { echo "no sites on the appliance: run the setup first" >&2; exit 1; }
first=$(head -1 "$tmp/sites" | cut -d' ' -f1)
site_pw() { case "$1" in *10.23.210.185*) echo "$HOME/portico-secrets/nyc1-pw" ;; *) echo "$HOME/portico-secrets/prism-pw" ;; esac; }

# 1. Tenants, then wait until each is active on its sites.
tenant() { # slug display contacts site...
  local slug="$1" display="$2" contacts="$3"; shift 3
  jq -n --arg s "$slug" --arg d "$display" --arg c "$contacts" --args '{slug:$s, display_name:$d, contacts:$c, sites:[$ARGS.positional[] | {site:.}]}' "$@" > "$tmp/t.json"
  local code; code=$(api POST /api/v1/tenants "$tmp/t.json")
  case "$code" in 20*) say ok "tenant $slug created on $*" ;; 409) say ok "tenant $slug exists" ;; *) say FAIL "tenant $slug: HTTP $code $(field .error)"; return ;; esac
  local st=""
  for _ in $(seq 1 120); do
    api GET "/api/v1/tenants/$slug" >/dev/null; st=$(field .status)
    case "$st" in active|failed) break ;; esac
    sleep 5
  done
  if [ "$st" = active ]; then say ok "tenant $slug active"; else say FAIL "tenant $slug $st: $(field .last_error)"; fi
}
mapfile -t ids < <(cut -d' ' -f1 "$tmp/sites")
tenant acme "ACME Cloud" ops@acme.example "${ids[@]}"
tenant globex "Globex" ops@globex.example "$first"

# 2. Users, through Portico; then a permanent password through a master
#    administrator of this run only.
pwfile() { local f="$S/$1-user-pw"; [ -s "$f" ] || { LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 21 > "$f"; printf 'Aa1' >> "$f"; }; echo "$f"; }
user() { # slug email first last
  jq -n --arg e "$2" --arg f "$3" --arg l "$4" '{email:$e, first_name:$f, last_name:$l}' > "$tmp/u.json"
  local code; code=$(api POST "/api/v1/tenants/$1/users" "$tmp/u.json")
  : > "$tmp/out"   # the temporary password is not kept
  case "$code" in 201) say ok "user $2 created" ;; 409) say ok "user $2 exists" ;; *) say FAIL "user $2: HTTP $code" ;; esac
}
user acme carol@acme.example Carol Acme
user globex dave@globex.example Dave Globex
kcpw="$tmp/kc-pw"; { LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 24; echo; } > "$kcpw"
# kc.sh reads $PORTICO_SECRETS/keycloak-admin-pw and signs in as kcadmin:
# both point at this run's administrator.
mkdir "$tmp/s"; for f in "$S"/*; do ln -s "$f" "$tmp/s/"; done; rm -f "$tmp/s/keycloak-admin-pw"; cp "$kcpw" "$tmp/s/keycloak-admin-pw"
# The password reaches the container on stdin only, never an argument.
if "${SSH[@]}" "sudo podman exec -i keycloak bash -c 'read -r PW; export PW; /opt/keycloak/bin/kc.sh bootstrap-admin user --username kcadmin --password:env PW --no-prompt --http-management-port=9001'" < "$kcpw" >"$tmp/kc.log" 2>&1; then
  say ok "temporary Keycloak administrator created"
  for u in acme:carol@acme.example globex:dave@globex.example; do
    slug=${u%%:*}; email=${u#*:}; pf=$(pwfile "$slug")
    id=$(PORTICO_SECRETS="$tmp/s" lab/saml/kc.sh GET "/$REALM/users?email=$email&exact=true" 2>/dev/null | jq -r '.[0].id // empty')
    jq -n --rawfile p "$pf" '{type:"password", value:($p|rtrimstr("\n")), temporary:false}' > "$tmp/pw.json"
    code=$(PORTICO_SECRETS="$tmp/s" lab/saml/kc.sh PUT "/$REALM/users/$id/reset-password" "$tmp/pw.json" 2>&1 >/dev/null | tail -1); rm -f "$tmp/pw.json"
    if [ "$code" = "HTTP 204" ]; then say ok "$email: permanent password in $pf"; else say FAIL "$email: reset-password $code"; fi
  done
  kid=$(PORTICO_SECRETS="$tmp/s" lab/saml/kc.sh GET "/master/users?username=kcadmin&exact=true" 2>/dev/null | jq -r '.[0].id // empty')
  code=$(PORTICO_SECRETS="$tmp/s" lab/saml/kc.sh DELETE "/master/users/$kid" 2>&1 >/dev/null | tail -1)
  if [ "$code" = "HTTP 204" ]; then say ok "temporary Keycloak administrator deleted"; else say FAIL "temporary Keycloak administrator NOT deleted ($code): delete kcadmin in the master realm"; fi
else
  say FAIL "temporary Keycloak administrator: $(tail -2 "$tmp/kc.log" | tr '\n' ' ')"
fi
rm -rf "$tmp/s" "$kcpw"

# 3. VMs in the tenants' projects, powered on.
vms() { # slug site name...
  local slug="$1" site="$2"; shift 2
  local url cl sc; read -r _ url cl sc < <(grep "^$site " "$tmp/sites")
  (
    PC_URL="$url"; PC_PASS="$(cat "$(site_pw "$url")")"; export PC_URL PC_PASS
    # Prism Central answers a list with 504 "upstream request timeout"
    # now and then (2026-10-27): a few more tries before giving up.
    p=""; img=""
    for _ in 1 2 3 4 5 6; do
      [ -n "$p" ] || p=$(project_extid "${LAB_PROJECT_PREFIX:-portico}-$slug" 2>/dev/null)
      [ -n "$img" ] || img=$(image_extid "$IMAGE" 2>/dev/null)
      if [ -n "$p" ] && [ -n "$img" ]; then break; fi
      sleep 10
    done
    [ -n "$p" ] || { say FAIL "$site: no project for $slug"; exit 1; }
    [ -n "$img" ] || { say FAIL "$site: no image $IMAGE"; exit 1; }
    for name in "$@"; do
      vm=$(vm_extid "$name")
      if [ -z "$vm" ]; then
        jq -n --arg n "$name" --arg c "$cl" --arg p "$p" --arg i "$img" --arg sc "$sc" '{
          name:$n, numSockets:1, numCoresPerSocket:1, memorySizeBytes:1073741824,
          cluster:{extId:$c}, project:{extId:$p},
          bootConfig:{"$objectType":"vmm.v4.ahv.config.LegacyBoot"},
          disks:[{diskAddress:{busType:"SCSI", index:0}, backingInfo:{"$objectType":"vmm.v4.ahv.config.VmDisk", diskSizeBytes:10737418240,
            storageContainer:{extId:$sc},
            dataSource:{reference:{"$objectType":"vmm.v4.ahv.config.ImageReference", imageExtId:$i}}}}]}' > "$tmp/vm.json"
        t=$(lab/pc.sh POST /api/vmm/v4.0/ahv/config/vms "$tmp/vm.json" 2>/dev/null | jq -r '.data.extId // empty')
        if [ -z "$t" ] || ! wait_task "$t" 300 >/dev/null 2>"$tmp/err"; then say FAIL "$site: VM $name not created $(head -c 300 "$tmp/err")"; continue; fi
        vm=$(vm_extid "$name")
      fi
      st=$(lab/pc.sh GET "/api/vmm/v4.0/ahv/config/vms/$vm" 2>/dev/null | jq -r .data.powerState)
      if [ "$st" != ON ]; then t=$(vm_action "$vm" power-on); wait_task "$t" 120 >/dev/null; fi
      say ok "$site: VM $name in project $slug, $(lab/pc.sh GET "/api/vmm/v4.0/ahv/config/vms/$vm" 2>/dev/null | jq -r .data.powerState)"
    done
    exit "$fail"
  ) || fail=1
}
second=$(sed -n 2p "$tmp/sites" | cut -d' ' -f1)
vms acme "$first" acme-web-01 acme-db-01
[ -n "$second" ] && vms acme "$second" acme-web-02
vms globex "$first" globex-app-01 globex-app-02

rm -rf "$tmp"
if [ $fail -eq 0 ]; then echo PASS; else echo FAIL; exit 1; fi
