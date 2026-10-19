#!/usr/bin/env bash
# Packer's shutdown command: remove the build user (its session is the one
# running this) and power off. Detached so the SSH session may close.
set -u
nohup bash -c 'sleep 2; userdel -rf build 2>/dev/null; rm -f /etc/sudoers.d/build; rm -rf /tmp/portico-build; sync; shutdown -P now' >/dev/null 2>&1 &
exit 0
