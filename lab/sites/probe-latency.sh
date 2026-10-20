#!/usr/bin/env bash
# Sites (write-up 21): how long Prism Central takes to answer the calls a
# site probe could use. Lab only. Uses lab/pc.sh (admin from lab/env.sh,
# or PC_API_KEY when set: the service account's view).
#   source lab/env.sh && lab/sites/probe-latency.sh [ROUNDS]
# Prints one line per round: name=HTTP code/seconds for each call, with a
# 60 s cap per call.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
rounds="${1:-10}"
names=(clusters clusters-limit1 domain-managers iam-users-limit1)
paths=(
  "/api/clustermgmt/v4.0/config/clusters"
  "/api/clustermgmt/v4.0/config/clusters?\$limit=1"
  "/api/prism/v4.0/config/domain-managers"
  "/api/iam/v4.0/authn/users?\$limit=1"
)
now() { perl -MTime::HiRes=time -e 'printf "%.2f", time'; }
for _ in $(seq 1 "$rounds"); do
  line="$(date +%H:%M:%S)"
  for i in "${!paths[@]}"; do
    t0=$(now)
    code=$(perl -e 'alarm shift; exec @ARGV' 60 lab/pc.sh GET "${paths[$i]}" 2>&1 >/dev/null | tail -1 | sed -n 's/^HTTP //p')
    t1=$(now)
    line="$line  ${names[$i]}=${code:-none}/$(echo "$t1 - $t0" | bc)s"
  done
  echo "$line"
  sleep 3
done
