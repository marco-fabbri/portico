#!/usr/bin/env bash
# Sites (write-up 21, D4): how often an open, untouched console tab sends
# a request through Portico, which decides whether such a tab counts as
# "active" (a request in the last two minutes, auth.ActiveWindow). Lab
# only. Reads Portico's access log on the appliance (paths, status and
# times only: the log never holds a header, a cookie or a token).
#   source lab/env.sh && lab/sites/console-polling.sh [MINUTES]
# Run it with one tenant browser on the public name, the console open and
# left alone for MINUTES (default 5). Prints the requests per path, the
# gaps between consecutive requests, and the longest gap. Lines that are
# not JSON (systemd's own) are skipped.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_SSH_USER:?}" "${LAB_PRISM_HOST:?}"
minutes="${1:-5}"
# shellcheck disable=SC2206 # LAB_SSH_OPTS is a list of options
O=(-o BatchMode=yes -o ConnectTimeout=5 -o LogLevel=ERROR ${LAB_SSH_OPTS:-})
tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
# shellcheck disable=SC2029 # $minutes expands here on purpose
ssh "${O[@]}" "$LAB_SSH_USER@$LAB_VM_IP" "sudo -n journalctl -u portico --since '-$minutes min' --no-pager -o cat" \
  | jq -rR --arg h "$LAB_PRISM_HOST" 'fromjson? | select(.msg=="request" and .host==$h and .session==true) | "\(.time) \(.status) \(.path)"' > "$tmp"
n=$(wc -l < "$tmp" | tr -d ' ')
echo "requests with a session on $LAB_PRISM_HOST in the last $minutes min: $n"
[ "$n" -gt 0 ] || exit 0
echo "--- per path (count, status)"
awk '{print $3, $2}' "$tmp" | sed -E 's#/[0-9a-f-]{36}#/{id}#g' | sort | uniq -c | sort -rn | head -20
echo "--- gaps between consecutive requests (seconds)"
python3 -c '
import sys, datetime as d
ts = [d.datetime.fromisoformat(l.split()[0][:26].rstrip("Z")) for l in open(sys.argv[1])]
gaps = [round((b - a).total_seconds(), 1) for a, b in zip(ts, ts[1:])]
big = [g for g in gaps if g >= 5]
print("gaps of 5 s or more:", big if big else "none")
print("longest gap: %s s" % (max(gaps) if gaps else "-"))
' "$tmp"
