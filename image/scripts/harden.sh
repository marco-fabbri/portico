#!/usr/bin/env bash
# OpenSCAP hardening of the appliance image (design 4.9): remediate with
# the tailored CIS Level 1 Server profile, then evaluate and write the
# report Packer downloads. Runs as root inside the build VM.
set -uo pipefail
B=/tmp/portico-build
DS=/usr/share/xml/scap/ssg/content/ssg-rl9-ds.xml
PROFILE=xccdf_org.ssgproject.content_profile_portico
TAILORING=$B/oscap/tailoring.xml
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
echo "harden: evaluation exit $?"
mv /root/sudoers.build /etc/sudoers.d/build
chmod 0644 "$B/oscap-report.html"
pass=$(grep -c '<result>pass</result>' "$B/oscap-results.xml" || true)
fail=$(grep -c '<result>fail</result>' "$B/oscap-results.xml" || true)
echo "harden: pass=$pass fail=$fail"
if [ "$fail" -gt 0 ]; then
  echo "harden: failing rules:"
  grep -B3 '<result>fail</result>' "$B/oscap-results.xml" | grep -o 'idref="[^"]*"' | sed 's/idref=//; s/"//g' | sort -u
fi
exit 0
