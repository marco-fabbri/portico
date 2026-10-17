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
