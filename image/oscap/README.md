# Hardening of the appliance image

The image build applies the **CIS Level 1 Server** profile of
`scap-security-guide` for Rocky Linux 9 with OpenSCAP: remediation first
(`oscap xccdf eval --remediate`), then an evaluation whose HTML report is
attached to the release as `portico-<version>-oscap.html`. The tailoring
file `tailoring.xml` extends that profile and unselects the rules listed
below, each with its reason; the report shows them as "notselected".

> **Scope and disclaimer**
>
> - The report describes the image as built. It is **not** a compliance
>   statement, and passing rules here does not make an appliance CIS
>   compliant in your environment: scan it yourself with a benchmark tool
>   after you configured it.
> - This project is not affiliated with, endorsed by or supported by the
>   Center for Internet Security (CIS). CIS and CIS Benchmarks are
>   trademarks of the Center for Internet Security, Inc.; no CIS Benchmark
>   document is distributed here. The rules come from the open source
>   `scap-security-guide` package of the distribution.

## Exceptions

| Rule | Why |
|---|---|
| `sysctl_net_ipv4_ip_forward` | Podman's bridge network (Keycloak, PostgreSQL) needs forwarding on the host; firewalld forwards nothing towards other networks |
| `ensure_root_password_configured` | No root password by design: tty1 runs the console wizard as root through a getty override (design 4.8); nobody logs in as root |
| `grub2_password` | Whoever reaches the console already gets the root wizard there; a bootloader password adds nothing and would have to be shared with every MSP |
| `accounts_password_last_change_is_in_past` | Evaluated on the build day, when every password change date is that day; meaningful on a running appliance, not in the build |

## Layout exceptions documented in the kickstart

`/var/lib/containers` is mounted `nodev,nosuid` without `noexec`:
container layers must execute. `/tmp` (tmpfs), `/var/tmp`, `/dev/shm`,
`/home`, `/var/log`, `/var/log/audit` and `/var/lib/portico` are `noexec`.

## Re-measuring

On a running appliance, as root:

```
oscap xccdf eval --profile xccdf_org.ssgproject.content_profile_portico \
  --tailoring-file /usr/share/portico/tailoring.xml --report /root/oscap.html \
  /usr/share/xml/scap/ssg/content/ssg-rl9-ds.xml
```
