#!/usr/bin/env bash
# Close the image: no cache, no log, no host key, no machine id, no
# network profile of the build, no build artefacts. The build user and
# its sudoers file go with poweroff.sh, which Packer runs last.
set -euo pipefail
dnf -y -q clean all
rm -rf /var/cache/dnf /var/lib/dnf/history*
rm -f /etc/ssh/ssh_host_*
truncate -s 0 /etc/machine-id
mkdir -p /var/lib/dbus && rm -f /var/lib/dbus/machine-id && ln -s /etc/machine-id /var/lib/dbus/machine-id
cloud-init clean --logs --seed
# Anaconda wrote a DHCP profile for the build network; the appliance gets
# its network from the console wizard or from cloud-init's bootcmd.
rm -f /etc/NetworkManager/system-connections/*.nmconnection
echo portico > /etc/hostname
rm -rf /var/log/anaconda /root/anaconda-ks.cfg /root/original-ks.cfg /root/ks-post.log /root/.bash_history
journalctl --rotate >/dev/null 2>&1 || true
journalctl --vacuum-time=1s >/dev/null 2>&1 || true
find /var/log -type f -name '*.log' -exec truncate -s 0 {} + 2>/dev/null || true
find /var/log -type f \( -name '*.gz' -o -name '*-????????' \) -delete 2>/dev/null || true
rm -rf /tmp/portico-build/portico /tmp/portico-build/deploy /tmp/portico-build/oscap /tmp/portico-build/oscap-* 
rm -f /home/build/.bash_history
restorecon -R /etc /var/lib/portico >/dev/null 2>&1 || true
sync
fstrim -av >/dev/null 2>&1 || true
# The image carries no secret: whatever the build generated goes, the first boot makes new ones.
rm -f /etc/portico/keycloak.env /etc/portico/tls/*
