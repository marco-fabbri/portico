# Lab inventory

Everything the lab created or touched on NTNX-01 and on the lab VM. Nothing
is left behind without a row here.

| object | name | created by task | cleanup |
|---|---|---|---|
| project | `_internal` (Default Project, extId all zeros) | pre-existing | never touch |
| user | `admin` (LOCAL) + 5 service accounts | pre-existing | never touch |
| authorization policy | `Super Admin_acp`, `Self-Service Admin_acp`, `Role_Mapping_Prism_Viewer_*`, `Role_Mapping_Prism_Admin_*` | pre-existing | never touch |
| authorization policy | `Project Manager_acp` (pre-existing; identity `portico` added by Task 7) | Task 7 | `lab/auth/create-service-account.sh delete` removes the identity |
| SAML identity provider | none | — | — |
| image | `rocky-9-genericcloud` (Rocky 9 GenericCloud, from URL) | Task 3 | delete from Prism Central image service when the lab ends |
| VM | `portico-lab` (2 vCPU, 6 GiB, 40 GiB, `10.23.210.190`, subnet MGMT) | Task 3 | `lab/vm/delete-vm.sh` |
| secrets (Mac) | `~/portico-secrets/keycloak-admin-pw`, `keycloak-db-pw`, `lab-ca.crt`, `lab-ca.key` | Task 4 | delete when the lab ends |
| VM services | podman quadlets `postgres`, `keycloak`, network `portico-lab`; nginx with lab cert; firewalld | Task 4 | gone with the VM |
| project | `portico-lab-tenant-a` (extId `735bdb5e-457a-5e55-85a2-0d021d87feda`) + resource group `portico-lab-tenant-a-rg` | Task 5/6 | `lab/projects/delete.sh portico-lab-tenant-a tenant-a` |
| SAML group (PC) | `tenant-a` + role membership Project Admin in tenant A: **removed by the M2e reset** (`reset-for-bootstrap.sh`); tenant A is a project with a VM and no members since 2026-10-18 | Task 6 | — |
| local user (PC) | `portico-lab-bob` (Project Admin of tenant A; password in `~/portico-secrets/bob-pw`) | Task 6 | `DELETE /api/iam/v4.0/authn/users/{extId}` when the lab ends |
| SAML IdP (PC) | `portico-lab-keycloak` (extId `247f2c3d-4566-52fe-84ed-48b14799bf05`, same as before the reset: Prism Central derives it from the name); since M2e created by `POST /api/v1/settings/bootstrap/saml` with the admin credentials, not shared with tenant A any more | M2e Task 6 | `lab/saml/pc-idp.sh delete` (deletes its SAML users first) or `lab/portico/reset-for-bootstrap.sh --yes` |
| Keycloak | realm `portico-lab`: since M2e created by `POST /api/v1/settings/bootstrap/keycloak` (the M0 realm with `alice@lab.portico.test` and group `tenant-a` was deleted by the reset; `~/portico-secrets/alice-pw` is stale), SAML client `https://prism.lab.portico.test/api/iam/authn` with the email and groups mappers created by the SAML bootstrap | M2e Task 6 | `lab/portico/reset-for-bootstrap.sh --yes`; gone with the VM |
| service account (PC) | `portico` (extId `c22f4a4b-4624-5e84-bff1-ea5e719aa1e5`); since M2e created by `POST /api/v1/settings/bootstrap/prism-central`, key `portico-20261018-165057` sealed in the store (no file on the Mac), member of `Project Manager_acp` | M2e Task 6 | `lab/auth/create-service-account.sh delete` or the reset script |
| subnet sharing (PC) | `MGMT` shared with project `portico-lab-tenant-a` | Task 8 prep | `POST /api/networking/v4.4/config/subnets/{extId}/$actions/unshare` |
| VM (PC) | `portico-lab-tenant-a-vm1` (1 vCPU, 1 GiB, in project tenant A, image rocky-9-genericcloud) | Task 8 prep | power off + `DELETE /api/vmm/v4.0/ahv/config/vms/{extId}` |
| SAML user (PC) | `alice@lab.portico.test` and `carol@lab.portico.test` (M0–M2d logins): **deleted by the M2e reset** (an IdP cannot be deleted while they exist, IAM-21016) | Task 5 | — |
| Keycloak OIDC client | `portico` in realm `portico-lab`, redirect `https://prism.lab.portico.test/.portico/callback`; since M2e created by the Keycloak bootstrap, secret sealed in the store (`~/portico-secrets/portico-oidc-secret` removed) | M2e Task 6 | gone with the realm |
| VM service | `portico.service` (user `portico`, `/usr/local/bin/portico`, `/etc/portico/`), nginx disabled; firewalld ports 8443 and 9440; since M2b port 8443 also serves the admin UI (`https://prism.lab.portico.test:8443/`, user `admin`) | M1 Task 8 | `lab/portico/undeploy.sh` restores nginx; gone with the VM |
| Keycloak client | `portico-admin` in realm `portico-lab` (service account with eight realm-management roles); since M2e created by the Keycloak bootstrap, secret sealed in the store (`~/portico-secrets/portico-kc-admin-secret` removed) | M2e Task 6 | gone with the realm |
| VM files | since M2d: `/etc/portico/portico.yaml` holds process settings only (`lab/portico/portico.store.yaml`); the runtime settings and the three secrets are in the store `/var/lib/portico/portico.db`, sealed with `/var/lib/portico/master.key` (portico:portico 0600, created by the binary); since M2e the store was filled by the bootstraps on a fresh deploy (`deploy.sh --fresh`); backups `portico.db.pre-2e-20261018-163750` (M2d document, tenant A) and `portico.db.pre-2e-20261018-164911` (a draft) next to it | M2a Task 6, M2d Task 6, M2e Task 6 | gone with the VM; a new VM is bootstrapped from the setup page or imports from `lab/portico/portico.yaml` |
| branding (store) | MSP name `Portico Lab MSP`, logo `lab/portico/portico-lab-logo.svg` (inert SVG) uploaded through `PUT /api/v1/settings/branding` | M2d Task 6 | gone with the VM |
| VM files | `/var/lib/portico/keycloak-theme/` (login theme written by Portico, mounted in the Keycloak container); realm `portico-lab` has `loginTheme=portico`; branding stored in the Portico store | M2c Tasks 0/2 | `lab/saml/theme-probe.sh reset` puts the default theme back; gone with the VM |
| secrets (Mac) | `~/portico-secrets/portico-admin-pw` (break-glass password of the admin API); `carol-tmp-pw`/`carol-pw` exist only between `acceptance.sh` and `acceptance.sh --delete` | M2a Task 6/7 | delete when the lab ends |
| tenant (API) | `tenant-b` → project `portico-lab-tenant-b`, groups `tenant-tenant-b`: exist only while `lab/tenants/acceptance.sh` runs; removed by `acceptance.sh --delete` | M2a Task 7 | `lab/tenants/acceptance.sh --delete` |
| Keycloak user | `carol@lab.portico.test` (created by `acceptance.sh`, removed by `acceptance.sh --delete`; Portico itself leaves tenant users in place) | M2a Task 7 | `lab/tenants/acceptance.sh --delete` |
| SAML user (PC) | `carol@lab.portico.test`, created by Prism Central at her first SAML login after each acceptance run | M2a Task 7 | `lab/saml/pc-idp.sh delete` removes it with the IdP; or `DELETE /api/iam/v4.1.b3/authn/users/{extId}` |
