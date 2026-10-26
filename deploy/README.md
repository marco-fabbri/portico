# Deployment files

What runs on an appliance or on a Rocky Linux 9 host of your own, and the
installer that puts it there (design 4.9).

| File | Installed as | Role |
|---|---|---|
| `install.sh` | | The installer: `install.sh --binary PATH \| --release VERSION [--no-start] [--render-only DIR]` |
| `portico.appliance.yaml` | `/etc/portico/portico.yaml` (only when absent) | Process settings of the appliance: listeners, TLS files, store; no runtime section, so a fresh host starts in setup mode |
| `portico.yaml.example` | | The full settings file with every runtime section, for the pre-created path (`portico settings import`) |
| `portico.service` | `/etc/systemd/system/portico.service` | The service, unprivileged, `Restart=always` |
| `portico-console.service` | `/etc/systemd/system/getty@tty1.service.d/override.conf` | The console wizard on tty1: network, break-glass password, status |
| `quadlets/*.container`, `quadlets/portico.network` | `/etc/containers/systemd/` | PostgreSQL and Keycloak as podman quadlets |
| `keycloak-hostname.path`, `.service` | `/etc/systemd/system/` | Restart Keycloak when Portico writes its public hostname on Apply |
| `portico-exposure.path`, `.service` | `/etc/systemd/system/` | Run `portico exposure apply` as root when the admin UI asks for a certificate, a self-signed pair or the tunnel |
| `cloudflared.service` | `/etc/systemd/system/cloudflared.service` | The tunnel connector, enabled by `portico exposure apply` in tunnel mode |
| `firewalld/portico.xml` | `/etc/firewalld/services/portico.xml` | Ports 443 and 8443 |
| `sshd/00-portico.conf`, `sysctl/60-portico.conf` | drop-ins | Keys only, no root; CIS network parameters |
| `tofu/nutanix/` | | OpenTofu module: the image and the VM on Prism Central |

## Two ways to get Portico

**The appliance image** (`portico-<version>.qcow2` on the GitHub release)
is a Rocky Linux 9 built from `image/` with this installer already run:
import it into Prism Central, create the VM (2 vCPU, 6 GiB, 40 GiB), boot,
set the network on the console and continue in the browser.

**An existing Rocky Linux 9 host** (a VM on any hypervisor, a cloud
instance): as root,

```
curl -fsSLO https://github.com/marco-fabbri/portico/releases/download/v<version>/install-<version>.tar.gz
curl -fsSLO https://github.com/marco-fabbri/portico/releases/download/v<version>/install-<version>.tar.gz.sha256
sha256sum -c install-<version>.tar.gz.sha256
tar -xzf install-<version>.tar.gz && cd deploy && ./install.sh --release <version>
```

The installer verifies the binary's checksum, installs everything above,
generates the Keycloak secrets and the internal TLS pair once, starts the
services and leaves the host in setup mode with the factory password
(`admin` / `portico/4u`, changed at the first login). Then open
`https://<address>:8443/`: the setup page does the exposure and the
Keycloak realm.

Re-running the installer upgrades the binary and the files and keeps
every secret, the store and the settings. Before it replaces an
installed binary it stops Portico and copies the store and the master
key to `/var/lib/portico/backup-<old version>-<time>/`; the backup of
the whole appliance is a snapshot of its VM (docs/operating.md,
Backup).

## What the installer never does

It writes no public name, no Prism Central address and no credential:
those come from the setup page and are sealed in the store. It opens
neither 9440 nor 8080 on the firewall. It does not harden the OS: the
image build does that with OpenSCAP (`image/`), and on your own host it is
your baseline.
