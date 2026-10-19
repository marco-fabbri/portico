# Portico appliance: Rocky Linux 9 kickstart (design 4.9). Run by Packer
# from the boot ISO on a 40 GiB disk; the layout follows CIS Level 1
# Server with the documented exceptions (image/oscap/README.md).
text
url --url=https://dl.rockylinux.org/pub/rocky/9/BaseOS/x86_64/os/
repo --name=AppStream --baseurl=https://dl.rockylinux.org/pub/rocky/9/AppStream/x86_64/os/
lang en_US.UTF-8
keyboard us
timezone UTC --utc
network --bootproto=dhcp --device=link --activate --hostname=portico
selinux --enforcing
firewall --enabled --ssh
services --enabled=NetworkManager,chronyd,auditd,rsyslog,firewalld
# No root password: the console wizard runs as root on tty1 through a
# getty override, SSH is key-only for the user cloud-init may add.
rootpw --lock
# The build user exists for Packer's SSH session only; scripts/poweroff.sh
# removes it before the image is closed.
user --name=build --groups=wheel --plaintext --password=portico-build
poweroff

%pre --erroronfail --log=/tmp/ks-pre.log
# Review Focus 5: a disk smaller than the layout stops the installation
# with the layout named, instead of a half-installed image. The check
# reports on the serial console so a build log shows what it saw.
exec >/dev/ttyS0 2>&1
echo "portico %pre: block devices:"
lsblk -dnb -o NAME,SIZE,TYPE 2>&1 || echo "lsblk failed"
size=""
for dev in /sys/block/vd* /sys/block/sd* /sys/block/nvme*; do
  [ -r "$dev/size" ] || continue
  sectors=$(cat "$dev/size")
  size=$((sectors * 512))
  echo "portico %pre: first disk $(basename "$dev") = $size bytes"
  break
done
min=$((39 * 1024 * 1024 * 1024))
if [ -z "$size" ]; then
  echo "portico %pre: no disk found under /sys/block; letting anaconda report it"
elif [ "$size" -lt "$min" ]; then
  msg="portico: the disk must be at least 40 GiB (/boot 1 GiB, / 10, /var 6, /var/log 4, /var/log/audit 2, /var/tmp 1, /home 1, /var/lib/containers 8, /var/lib/portico 4); found $((size / 1024 / 1024 / 1024)) GiB"
  echo "$msg"
  echo "$msg" > /dev/tty1 2>/dev/null || true
  exit 1
else
  echo "portico %pre: disk size ok"
fi
%end

zerombr
clearpart --all --initlabel
bootloader --location=mbr --timeout=1 --append="audit=1 audit_backlog_limit=8192"
part biosboot --fstype=biosboot --size=1
part /boot --fstype=xfs --size=1024 --fsoptions="nodev,nosuid"
part pv.01 --fstype=lvmpv --size=1 --grow
volgroup portico pv.01
logvol / --vgname=portico --name=root --fstype=xfs --size=10240
logvol /var --vgname=portico --name=var --fstype=xfs --size=6144 --fsoptions="nodev,nosuid"
logvol /var/log --vgname=portico --name=log --fstype=xfs --size=4096 --fsoptions="nodev,nosuid,noexec"
logvol /var/log/audit --vgname=portico --name=audit --fstype=xfs --size=2048 --fsoptions="nodev,nosuid,noexec"
logvol /var/tmp --vgname=portico --name=vartmp --fstype=xfs --size=1024 --fsoptions="nodev,nosuid,noexec"
logvol /home --vgname=portico --name=home --fstype=xfs --size=1024 --fsoptions="nodev,nosuid,noexec"
# Container layers need exec: the documented exception (design 4.9).
logvol /var/lib/containers --vgname=portico --name=containers --fstype=xfs --size=8192 --fsoptions="nodev,nosuid"
logvol /var/lib/portico --vgname=portico --name=portico --fstype=xfs --size=4096 --fsoptions="nodev,nosuid,noexec"

%packages --excludedocs --inst-langs=en
@core
cloud-init
qemu-guest-agent
NetworkManager
podman
firewalld
openscap-scanner
scap-security-guide
chrony
audit
rsyslog
openssl
policycoreutils-python-utils
curl
tar
sudo
-cockpit*
-iwl*-firmware
-plymouth*
%end

%post --log=/root/ks-post.log
# Packer's session: passwordless sudo for the build user, removed at the end.
echo 'build ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/build
chmod 0440 /etc/sudoers.d/build
# /tmp on tmpfs and /dev/shm, both noexec (CIS 1.1).
mkdir -p /etc/systemd/system/tmp.mount.d
cat > /etc/systemd/system/tmp.mount.d/options.conf <<'TMP'
[Mount]
Options=mode=1777,strictatime,nosuid,nodev,noexec
TMP
systemctl unmask tmp.mount
systemctl enable tmp.mount
grep -q ' /dev/shm ' /etc/fstab || echo 'tmpfs /dev/shm tmpfs defaults,nodev,nosuid,noexec 0 0' >> /etc/fstab
# cloud-init: the Nutanix guest customization (ConfigDrive, NoCloud) may
# add a user and an SSH key and set the network in bootcmd (write-up 02);
# cloud-init's own network rendering is off so that the console wizard's
# keyfiles are the only ones.
cat > /etc/cloud/cloud.cfg.d/90-portico.cfg <<'CI'
datasource_list: [ NoCloud, ConfigDrive, None ]
network:
  config: disabled
disable_root: true
ssh_pwauth: false
CI
systemctl enable qemu-guest-agent cloud-init cloud-init-local cloud-config cloud-final
%end
