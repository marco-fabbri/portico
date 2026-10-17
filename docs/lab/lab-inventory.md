# Lab inventory

Everything the lab created or touched on NTNX-01 and on the lab VM. Nothing
is left behind without a row here.

| object | name | created by task | cleanup |
|---|---|---|---|
| project | `_internal` (Default Project, extId all zeros) | pre-existing | never touch |
| user | `admin` (LOCAL) + 5 service accounts | pre-existing | never touch |
| authorization policy | `Super Admin_acp`, `Self-Service Admin_acp`, `Project Manager_acp`, `Role_Mapping_Prism_Viewer_*`, `Role_Mapping_Prism_Admin_*` | pre-existing | never touch |
| SAML identity provider | none | — | — |
| image | `rocky-9-genericcloud` (Rocky 9 GenericCloud, from URL) | Task 3 | delete from Prism Central image service when the lab ends |
| VM | `portico-lab` (2 vCPU, 6 GiB, 40 GiB, `10.23.210.190`, subnet MGMT) | Task 3 | `lab/vm/delete-vm.sh` |
| secrets (Mac) | `~/portico-secrets/keycloak-admin-pw`, `keycloak-db-pw`, `lab-ca.crt`, `lab-ca.key` | Task 4 | delete when the lab ends |
| VM services | podman quadlets `postgres`, `keycloak`, network `portico-lab`; nginx with lab cert; firewalld | Task 4 | gone with the VM |
| project | `portico-lab-tenant-a` (extId `735bdb5e-457a-5e55-85a2-0d021d87feda`) + resource group `portico-lab-tenant-a-rg` | Task 5/6 | `lab/projects/delete.sh portico-lab-tenant-a tenant-a` |
| SAML group (PC) | `tenant-a` (idp portico-lab-keycloak) + role membership Project Admin in tenant A | Task 6 | removed by the delete above |
| local user (PC) | `portico-lab-bob` (Project Admin of tenant A; password in `~/portico-secrets/bob-pw`) | Task 6 | `DELETE /api/iam/v4.0/authn/users/{extId}` when the lab ends |
| SAML IdP (PC) | `portico-lab-keycloak` (extId `247f2c3d-4566-52fe-84ed-48b14799bf05`, redirectURL `https://prism.lab.portico.test`, shared with tenant A) | Task 5 | `lab/saml/pc-idp.sh delete` |
| Keycloak | realm `portico-lab`, groups `tenant-a`, `tenant-b`, user `alice@lab.portico.test` (password `~/portico-secrets/alice-pw`), SAML client for Prism Central | Task 5/6 | gone with the VM |
| service account (PC) | `portico` (extId `c22f4a4b-4624-5e84-bff1-ea5e719aa1e5`), API key `portico-lab-key` in `~/portico-secrets/portico-sa-key`, member of `Project Manager_acp` | Task 7 | `lab/auth/create-service-account.sh delete` |
| subnet sharing (PC) | `MGMT` shared with project `portico-lab-tenant-a` | Task 8 prep | `POST /api/networking/v4.4/config/subnets/{extId}/$actions/unshare` |
| VM (PC) | `portico-lab-tenant-a-vm1` (1 vCPU, 1 GiB, in project tenant A, image rocky-9-genericcloud) | Task 8 prep | power off + `DELETE /api/vmm/v4.0/ahv/config/vms/{extId}` |
