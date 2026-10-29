#!/usr/bin/env bash
# Give the `portico` service account what Project Manager lacks for a
# tenant's ready network (write-up 34): sharing the site's external subnet
# with a tenant's project, and the floating IP operations.
#   source lab/env.sh && lab/auth/networking-role.sh create|show|delete
# create: a custom role "Portico Networking" with Share_Subnet,
# Unshare_Subnet, the four floating IP operations and the VPC route table
# ones a default route needs (Project Manager has none of them, measured),
# and an authorization policy that binds it to `portico`, with the subnet
# operations limited to the external subnet (EXT_SUBNET, default
# external-nat). An existing role gets the operations of this list. Optional:
# FIP_SCOPE=all (default) or a project name, to limit the floating IP
# operations to one project. Lab only.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
SA_NAME="portico"; ROLE_NAME="Portico Networking"; POLICY_NAME="Portico Networking_portico"
OPS=(Share_Subnet Unshare_Subnet View_Floating_Ip Create_Floating_Ip Update_Floating_Ip Delete_Floating_Ip
  View_VPC_Route_Table View_Route_Table_Route Create_Route_Table_Route Delete_Route_Table_Route)
NET=/api/networking/v4.4/config
ext_name="${EXT_SUBNET:-external-nat}"
fip_scope="${FIP_SCOPE:-all}"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

uri() { jq -rn --arg n "$1" '$n|@uri'; }
op_extid() { lab/pc.sh GET "$IAM1/authz/operations?\$filter=displayName%20eq%20'$1'" 2>/dev/null | jq -r '.data[0].extId // empty'; }
sa_extid() { lab/pc.sh GET "$IAM0/authn/users" 2>/dev/null | jq -r --arg n "$SA_NAME" '.data[] | select(.username==$n) | .extId'; }
policy_extid() { lab/pc.sh GET "$IAM1/authz/authorization-policies?\$filter=displayName%20eq%20'$(uri "$POLICY_NAME")'" 2>/dev/null | jq -r '.data[0].extId // empty'; }
subnet_extid() { lab/pc.sh GET "$NET/subnets" 2>/dev/null | jq -r --arg n "$1" '.data[]? | select(.name==$n) | .extId'; }

# remove PATH LABEL: DELETE with the entity's ETag
remove() {
  local cf; cf=$(mktemp)
  IF_MATCH="$(etag_of "$1")" lab/pc.sh DELETE "$1" >/dev/null 2>"$cf"
  echo "delete $2: $(tr -d '\n' <"$cf")"; rm -f "$cf"
}

case "${1:-}" in
  create)
    sa=$(sa_extid); [ -n "$sa" ] || { echo "no service account $SA_NAME: run lab/auth/create-service-account.sh" >&2; exit 1; }
    ext=$(subnet_extid "$ext_name"); [ -n "$ext" ] || { echo "no subnet $ext_name" >&2; exit 1; }
    ops=()
    for o in "${OPS[@]}"; do
      id=$(op_extid "$o"); [ -n "$id" ] || { echo "no operation $o on this Prism Central" >&2; exit 1; }
      ops+=("$id")
    done
    role=$(role_extid "$ROLE_NAME")
    if [ -n "$role" ]; then
      lab/pc.sh GET "$IAM1/authz/roles/$role" 2>/dev/null | jq '.data | {displayName, description, operations}' > "$tmp/cur.json"
      if [ "$(jq -c '.operations | sort' "$tmp/cur.json")" = "$(jq -cn '$ARGS.positional | sort' --args "${ops[@]}")" ]; then
        echo "role exists: $role"
      else
        jq '.operations = $ARGS.positional' --args "${ops[@]}" "$tmp/cur.json" > "$tmp/role.json"
        IF_MATCH="$(etag_of "$IAM1/authz/roles/$role")" lab/pc.sh PUT "$IAM1/authz/roles/$role" "$tmp/role.json" >/dev/null 2>"$tmp/code"
        echo "update role: $(tr -d '\n' <"$tmp/code") ${#ops[@]} operations"
      fi
    else
      jq -n --arg n "$ROLE_NAME" '{displayName:$n, description:"Portico: share the external subnet with tenant projects, manage floating IPs", operations:$ARGS.positional}' --args "${ops[@]}" > "$tmp/role.json"
      code=$(post "$IAM1/authz/roles" "$tmp/role.json" "$tmp/resp") || { echo "create role: $code $(jq -c '.data.error // .' "$tmp/resp")" >&2; exit 1; }
      role=$(jq -r '.data.extId' "$tmp/resp"); echo "create role: $code $role"
    fi
    if [ "$fip_scope" = all ]; then
      fip_filter='{"floating_ip":{"*":{"eq":"*"}}}'
    else
      p=$(project_extid "$fip_scope"); [ -n "$p" ] || { echo "no project $fip_scope" >&2; exit 1; }
      fip_filter=$(jq -cn --arg p "$p" '{floating_ip:{project_reference:{anyof:[$p]}}}')
    fi
    pol=$(policy_extid)
    [ -z "$pol" ] || remove "$IAM1/authz/authorization-policies/$pol" "previous policy"
    jq -n --arg n "$POLICY_NAME" --arg r "$role" --arg u "$sa" --arg s "$ext" --argjson f "$fip_filter" '{
        displayName:$n, description:"Portico: networking for tenant ready networks",
        authorizationPolicyType:"USER_DEFINED", role:$r,
        identities:[{identityFilter:{user:{uuid:{anyof:[$u]}}}}],
        entities:[{entityFilter:{subnet:{uuid:{anyof:[$s]}}}}, {entityFilter:$f}, {entityFilter:{vpc_route_table:{"*":{"eq":"*"}}}}]}' > "$tmp/pol.json"
    code=$(post "$IAM1/authz/authorization-policies" "$tmp/pol.json" "$tmp/resp") || { echo "create policy: $code $(jq -c '.data.error // .' "$tmp/resp")" >&2; exit 1; }
    echo "create policy: $code $(jq -r '.data.extId' "$tmp/resp") (floating IPs: $fip_scope)"
    ;;
  show)
    pol=$(policy_extid)
    [ -n "$pol" ] && lab/pc.sh GET "$IAM1/authz/authorization-policies/$pol" 2>/dev/null | jq -c '.data | {displayName, role, entities: [.entities[].entityFilter]}'
    role=$(role_extid "$ROLE_NAME")
    [ -n "$role" ] && lab/pc.sh GET "$IAM1/authz/roles/$role" 2>/dev/null | jq -c '.data | {displayName, operations: (.operations|length)}'
    [ -n "$pol$role" ] || echo "no $ROLE_NAME role or policy"
    ;;
  delete)
    pol=$(policy_extid); [ -z "$pol" ] || remove "$IAM1/authz/authorization-policies/$pol" "policy $POLICY_NAME"
    role=$(role_extid "$ROLE_NAME"); [ -z "$role" ] || remove "$IAM1/authz/roles/$role" "role $ROLE_NAME"
    ;;
  *) echo "usage: $0 create|show|delete" >&2; exit 2 ;;
esac
