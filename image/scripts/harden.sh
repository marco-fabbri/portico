#!/usr/bin/env bash
# OpenSCAP hardening of the appliance image (design 4.9): remediate with
# the tailored CIS Level 1 Server profile, then evaluate and write the
# report Packer downloads. Runs as root inside the build VM. The build
# fails when a rule fails that image/oscap/expected-failures.txt does not
# list, so a regression never ships unnoticed (Milestone 5).
set -uo pipefail
B=/tmp/portico-build
DS=/usr/share/xml/scap/ssg/content/ssg-rl9-ds.xml
PROFILE=xccdf_org.ssgproject.content_profile_portico
TAILORING=$B/oscap/tailoring.xml
EXPECTED=$B/oscap/expected-failures.txt

# oscap_regressions RESULTS EXPECTED: compares the failing rules of an
# OpenSCAP results file with the expected list (one rule id per line,
# "#" comments and blank lines ignored). Prints every unexpected failure
# as "harden: new failing rule: <id>" and returns 1 when there is one;
# an expected failure that passes is reported and is not a failure.
oscap_regressions() {
  local results=$1 expected=$2 failing listed id rc=0
  failing=$(awk '/<rule-result /{match($0, /idref="[^"]*"/); id=substr($0, RSTART+7, RLENGTH-8)} /<result>fail<\/result>/{print id}' "$results" | sort -u)
  listed=$(sed 's/#.*//; s/[[:space:]]*$//; /^$/d' "$expected" | sort -u)
  for id in $failing; do
    if ! printf '%s\n' "$listed" | grep -qx "$id"; then echo "harden: new failing rule: $id"; rc=1; fi
  done
  for id in $listed; do
    if ! printf '%s\n' "$failing" | grep -qx "$id"; then echo "harden: expected failure now passes: $id"; fi
  done
  return $rc
}

# oscap_results_ok RESULTS EVAL_EXIT: a scan that did not run is not a
# clean scan. The evaluation's exit 0 (all pass) and 2 (some rules fail)
# are outcomes; 1 is an error. The results file must exist and hold at
# least one passing rule.
oscap_results_ok() {
  local results=$1 eval_exit=$2 pass
  if [ "$eval_exit" != 0 ] && [ "$eval_exit" != 2 ]; then echo "harden: evaluation failed (exit $eval_exit), see oscap-eval.log" >&2; return 1; fi
  if [ ! -s "$results" ]; then echo "harden: no results file: the evaluation did not run" >&2; return 1; fi
  pass=$(grep -c '<result>pass</result>' "$results" || true)
  if [ "$pass" -eq 0 ]; then echo "harden: no rule passed: the evaluation did not run on the profile" >&2; return 1; fi
  return 0
}

main() {
  install -d -m 0755 /usr/share/portico
  install -m 0644 "$TAILORING" /usr/share/portico/tailoring.xml
  # The build user's passwordless sudo is not part of the image: it is set
  # aside for the remediation and the evaluation, and restored for the
  # steps Packer still has to run (cleanup, power off).
  mv /etc/sudoers.d/build /root/sudoers.build
  echo "harden: remediation"
  oscap xccdf eval --profile "$PROFILE" --tailoring-file "$TAILORING" --remediate --results "$B/oscap-remediate.xml" "$DS" > "$B/oscap-remediate.log" 2>&1
  echo "harden: remediation exit $? (2 = some rules still fail, see the report)"
  echo "harden: evaluation"
  oscap xccdf eval --profile "$PROFILE" --tailoring-file "$TAILORING" --results "$B/oscap-results.xml" --report "$B/oscap-report.html" "$DS" > "$B/oscap-eval.log" 2>&1
  eval_exit=$?
  echo "harden: evaluation exit $eval_exit"
  mv /root/sudoers.build /etc/sudoers.d/build
  if ! oscap_results_ok "$B/oscap-results.xml" "$eval_exit"; then exit 1; fi
  chmod 0644 "$B/oscap-report.html"
  pass=$(grep -c '<result>pass</result>' "$B/oscap-results.xml" || true)
  fail=$(grep -c '<result>fail</result>' "$B/oscap-results.xml" || true)
  echo "harden: pass=$pass fail=$fail"
  if ! oscap_regressions "$B/oscap-results.xml" "$EXPECTED"; then
    echo "harden: a rule fails that image/oscap/expected-failures.txt does not list: fix it or list it with its reason" >&2
    exit 1
  fi
  exit 0
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then main "$@"; fi
