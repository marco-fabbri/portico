#!/usr/bin/env bash
# Sites measurements (write-up 20, D1 and D2, static part): which Prism
# Central cookies the console's JavaScript reads, and what it keeps in
# browser storage. Lab only. Uses the jar of a tenant login
# (lab/sites/tenant-login.sh SLUG --keep-jar DIR): downloads the console's
# script bundles through Portico and greps them. Prints names and counts,
# never a cookie value.
#   source lab/env.sh && lab/sites/console-static.sh DIR
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_PRISM_HOST:?}"
d="${1:?usage: console-static.sh JAR_DIR}"; jar="$d/jar"
[ -s "$jar" ] || { echo "no jar in $d" >&2; exit 1; }
js="$d/js"; mkdir -p "$js"
CURL=(curl -sk -b "$jar" --resolve "$LAB_PRISM_HOST:443:$LAB_VM_IP")
base="https://$LAB_PRISM_HOST"
"${CURL[@]}" -o "$d/console.html" "$base/console/"
srcs=$(grep -oE '(src|href)="[^"]+\.js[^"]*"' "$d/console.html" | sed -E 's/^(src|href)="//; s/"$//' | sort -u)
n=0
for s in $srcs; do
  case "$s" in http*) u="$s" ;; /*) u="$base$s" ;; *) u="$base/console/$s" ;; esac
  n=$((n + 1)); "${CURL[@]}" -o "$js/$n.js" "$u"
done
# Bundles load further chunks: follow one level of "*.js" names found in them.
for f in "$js"/*.js; do
  grep -oE '"[A-Za-z0-9_./-]+\.chunk\.js"|"[A-Za-z0-9_./-]+\.bundle\.js"' "$f" 2>/dev/null
done | tr -d '"' | sort -u | head -200 | while read -r c; do
  n=$((n + 1)); case "$c" in /*) u="$base$c" ;; *) u="$base/console/$c" ;; esac
  "${CURL[@]}" -o "$js/chunk-$(echo "$c" | tr '/' '_')" "$u"
done
echo "bundles downloaded: $(find "$js" -type f | wc -l | tr -d ' ') ($(du -sh "$js" | cut -f1))"
echo "--- document.cookie reads: $(grep -o 'document\.cookie' "$js"/* 2>/dev/null | wc -l | tr -d ' ')"
echo "--- cookie names of the jar, occurrences in the bundles (a read is likely where the count is not zero)"
awk -v h="$LAB_PRISM_HOST" -F'\t' 'NF==7 && index($1,h) {print $6}' "$jar" | sort -u | while read -r name; do
  printf '       %-28s %s\n' "$name" "$(grep -oF "$name" "$js"/* 2>/dev/null | wc -l | tr -d ' ')"
done
echo "--- localStorage / sessionStorage keys (literal keys only)"
grep -ohE '(localStorage|sessionStorage)\.(setItem|getItem|removeItem)\(["'"'"'][^"'"'"']+' "$js"/* 2>/dev/null | sed -E 's/\((["'"'"'])/ /' | sort | uniq -c | sort -rn | head -40
echo "--- indexedDB.open names"
grep -ohE 'indexedDB\.open\(["'"'"'][^"'"'"']+' "$js"/* 2>/dev/null | sort -u | head
