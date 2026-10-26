# Milestone 6: First Release v0.1.0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish `v0.1.0`, the first installable release of Portico:
the release pipeline that has never run on a tag, the documents an MSP
installs and upgrades from, the known limits, and a rehearsal on the
appliance before the real tag.

**Architecture:** Two branches cut from `main`, one pull request each:
`m6-release-pipeline` (the installer's upgrade backup and the release
job of `image.yml`) and `m6-release-docs` (release notes, install guide,
README, operating guide, design). Then a rehearsal tag `v0.1.0-rc.2`
that exercises the release job end to end as a draft, measured in
write-up 30, and the tag `v0.1.0`, also published as a draft that Marco
reviews and publishes himself. The two branches share no file, so they
merge in any order; the rehearsal needs both merged.

**Tech Stack:** bash (`deploy/install.sh`, `lab/test_scripts.sh`),
GitHub Actions (`.github/workflows/image.yml`,
`softprops/action-gh-release` already pinned), Markdown. The appliance
on NTNX-01 (`10.23.210.191`, `dev-f37aafa` today), sites `az-it-mi1` and
`az-us-nyc1`, tenant `acme`.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md` §9
(Milestone 6: "README, install guide, release artefacts, known limits
documented; tag `v0.1.0`"), §4.9 (appliance image: output, updates),
§5; `docs/backlog.md` (input of the known limits); write-up 29 (what
the general test found).

## Global Constraints

- Everything on disk in English. Chat with Marco in Italian.
- No "lab" in what an MSP reads in the product documents (README,
  `docs/install.md`, `docs/operating.md`, the release notes): say "test
  cluster" or name the version measured. Links to `lab/` and `docs/lab/`
  and the `LAB_*` variables stay.
- Never in the repository: the demo MSP's name, texts or palette,
  `colore.png`, anything from `kb/`, what the test hardware is for.
- Example names in documents: `portal.msp.example` and `id.msp.example`
  (the placeholders of `deploy/portico.yaml.example` and the setup page).
- Supported baseline: Prism Central pc.7.6 and later, Projects 2.0.
- Security rules quoted unchanged wherever a document states them: "the
  public path fails closed", "local Prism Central accounts never
  authenticate from outside", "Portico never stores a human
  administrator's credentials", "the master key never leaves the
  appliance".
- Commits with `git -c user.email=9093189+marco-fabbri@users.noreply.github.com commit`
  and the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`;
  PR bodies end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- Merge with `gh pr merge N --rebase --delete-branch`, only after
  Marco's OK. Every tag push, release publication and release deletion
  only after Marco's OK, each one asked separately.

## Decisions taken in this plan (Marco reviews them first)

1. **Backup for disaster recovery is a Nutanix snapshot of the VM.** The
   documented backup today copies `/var/lib/portico` only, and the
   install guide's "new image, restore the directory" path would lose
   Keycloak's database (the realm, its clients, every tenant user),
   which lives in the podman volume `postgres-data`. Copying the master
   key off the appliance would break "the master key never leaves the
   appliance". A snapshot or a protection policy in Prism Central saves
   the whole VM, Keycloak included, and keeps the key inside it; an MSP
   on Nutanix already has the tool. The "new image and restore" path is
   withdrawn for v0.1.0 and becomes a known limit with a backlog line.
2. **The installer takes the upgrade backup itself.** Before it replaces
   an existing binary it stops Portico and copies the store and the
   master key to `/var/lib/portico/backup-<old version>-<time>/` (root,
   0700), as `lab/portico/upgrade-appliance.sh` does today. A store
   migration that goes wrong is then undone without a snapshot. The
   lab script stops doing its own copy.
3. **The release job publishes a draft.** The tag builds the image and
   uploads the assets to a draft release whose text is
   `docs/releases/v<major.minor.patch>.md`; Marco reads it on GitHub and
   presses Publish. A tag with a suffix (`-rc.2`) is marked pre-release.
4. **A rehearsal tag before the real one.** The release job has never
   run. `v0.1.0-rc.2` exercises it; its draft is deleted afterwards
   (Marco's OK), the tag stays as history.

## Review Focus

1. An MSP following `docs/install.md` word by word meets a label that
   the setup page or the console does not show: every quoted UI label
   in the guide must exist in `internal/web/templates/` or
   `internal/console/` (Task 4, step 3).
2. `install.sh` run on a host where Portico was never installed, or
   installed but never started (no store yet): the backup step must
   skip silently and the install must go on (Task 1, step 1, cases b
   and c).
3. A tag whose release notes file is missing: the workflow must fail in
   the first minute, not after the 13-minute image build (Task 2,
   step 1).
4. The checksum files on the release must verify with
   `sha256sum -c <file>.sha256` in the directory of the downloads, the
   install tarball included (it has none today) (Task 2, step 1).
5. Backups taken by repeated upgrades accumulate under
   `/var/lib/portico` on a 40 GiB disk: the operating guide says where
   they are and that the MSP removes old ones (Task 5).

---

## Branch `m6-release-pipeline`

### Task 1: The installer backs up the store and the key before an upgrade

**Files:**
- Modify: `deploy/install.sh` (new step before "3. The binary")
- Modify: `lab/portico/upgrade-appliance.sh` (drop its own backup)
- Test: `lab/test_scripts.sh`

**Interfaces:**
- Produces: the directory `/var/lib/portico/backup-<version>-<YYYYmmdd-HHMMSS>/`
  holding `portico.db`, `portico.db-wal` and `portico.db-shm` when
  present, and `master.key`; the log line
  `install.sh: backup of the store and the master key in <dir>`.
  `<version>` is the second field of `/usr/local/bin/portico version`,
  or `unknown` when that fails. Tasks 4, 5 and 6 quote this path.

- [ ] **Step 1: Write the failing checks in `lab/test_scripts.sh`**

Three cases with `--render-only` into fresh temp dirs and the test
binary the file already builds (`$bin`):

  a. Pre-create `$d/usr/local/bin/portico` (a copy of `$bin`),
     `$d/var/lib/portico/portico.db` and `$d/var/lib/portico/master.key`
     (any content), run the installer: `ok "install.sh backs up the
     store and the key before an upgrade"` when exactly one
     `$d/var/lib/portico/backup-*/` exists holding both files with the
     same bytes (`cmp`), else `ko`.
  b. A fresh `$d` (no binary, no store): `ok "install.sh takes no
     backup on a first install"` when no `backup-*` directory exists and
     the installer exited 0.
  c. Binary present, no `portico.db`: `ok "install.sh takes no backup
     without a store"`, same conditions as b.

- [ ] **Step 2: Run them to see them fail**

Run: `bash lab/test_scripts.sh > "$WS/ts.out" 2>&1; echo rc=$?; grep -c '^ko' "$WS/ts.out"; grep 'backup' "$WS/ts.out"`
Expected: case a `ko`; b and c `ok` (nothing makes a backup yet); rc non-zero.

- [ ] **Step 3: Implement `backup_state` in `deploy/install.sh`**

Called just before "3. The binary", only when
`$ROOT/usr/local/bin/portico` exists and `$ROOT/var/lib/portico/portico.db`
exists. Outside `--render-only` it runs `systemctl stop portico.service`
first (SQLite's WAL is consistent only with the writer stopped); in
render mode it logs `would stop: portico` and copies. Directory created
with `install -d -m 0700` (owner root:root outside render mode), files
copied with `cp -p`. The log line of the Interfaces block.

- [ ] **Step 4: Remove the copy from `lab/portico/upgrade-appliance.sh`**

The remote block keeps the version check and the `tail` of the
installer's output, and greps the installer's backup line instead of
building its own path; the header comment says the installer does it.

- [ ] **Step 5: Run the checks**

Run: `bash lab/test_scripts.sh > "$WS/ts.out" 2>&1; echo rc=$?; grep '^ko' "$WS/ts.out"`
Expected: rc=0, no `ko` line; shellcheck clean (the file runs it).

- [ ] **Step 6: Commit**

```bash
git add deploy/install.sh lab/portico/upgrade-appliance.sh lab/test_scripts.sh
git commit -m "feat(deploy): the installer backs up the store and the master key before an upgrade"
```

### Task 2: The release job publishes a draft with our notes and every checksum

**Files:**
- Modify: `.github/workflows/image.yml`
- Test: `lab/test_scripts.sh`

**Interfaces:**
- Consumes: the notes file `docs/releases/v<X.Y.Z>.md` (written in
  Task 3 on the other branch; the rehearsal of Task 7 needs both).
- Produces: release assets `portico-<v>.qcow2`, `portico-<v>-oscap.html`,
  `portico-<v>-linux-amd64.tar.gz`, `install-<v>.tar.gz`, each with a
  `.sha256` in the `<sum>  <name>` format; a draft release.

- [ ] **Step 1: Write the failing checks in `lab/test_scripts.sh`**

Text checks on `image.yml`, one `ok`/`ko` each:
  - "the release is a draft": the release step has `draft: true`;
  - "the release text is docs/releases": `body_path:` names
    `docs/releases/` and `generate_release_notes: true` is gone;
  - "a suffixed tag is a pre-release": `prerelease:` is computed from
    the tag containing `-`;
  - "the install tarball has a checksum": `install-"$VER".tar.gz.sha256`
    (or the equivalent expression) appears in the Collect step;
  - "a tag without notes fails before the build": a step that tests the
    notes file exists, placed before the step named
    `Packer build (install, then provision)`.

- [ ] **Step 2: Run them to see them fail**

Run: `bash lab/test_scripts.sh > "$WS/ts.out" 2>&1; grep -E 'draft|release text|pre-release|install tarball|without notes' "$WS/ts.out"`
Expected: five `ko` lines.

- [ ] **Step 3: Edit `image.yml`**

- In the image job, right after "Version": on a tag only
  (`if: startsWith(github.ref, 'refs/tags/v')`), compute
  `NOTES=docs/releases/v${VER%%-*}.md`, fail with
  `::error::release notes $NOTES missing` when absent.
- In Collect: `sha256sum "install-$VER.tar.gz" > "install-$VER.tar.gz.sha256"`
  together with the others, before the `sha256sum -c`.
- In the release job: `actions/checkout` with the same pinned commit as
  the image job (for the notes file), the same `NOTES` computation from
  `github.ref_name`, then the release step with `draft: true`,
  `prerelease: ${{ contains(github.ref_name, '-') }}`,
  `body_path: ${{ env.NOTES }}`, `files: release/*`, no
  `generate_release_notes`.

- [ ] **Step 4: Run the checks**

Run: `bash lab/test_scripts.sh > "$WS/ts.out" 2>&1; echo rc=$?; grep '^ko' "$WS/ts.out"`
Expected: rc=0, no `ko`. Also `actionlint` if installed, else skip and
say so in the PR.

- [ ] **Step 5: Commit, push, PR**

```bash
git add .github/workflows/image.yml lab/test_scripts.sh
git commit -m "ci(release): a draft release with the notes of docs/releases and a checksum for every asset"
```
Push `m6-release-pipeline`, open the PR (Tasks 1 and 2), wait for a
green CI, ask Marco for the merge.

---

## Branch `m6-release-docs`

### Task 3: Release notes and known limits

**Files:**
- Create: `docs/releases/v0.1.0.md`
- Modify: `docs/backlog.md` (header paragraph; the new line of
  decision 1)

**Interfaces:**
- Produces: `docs/releases/v0.1.0.md`, read by the release job of
  Task 2 and linked from the README (Task 5) and the install guide
  (Task 4).

- [ ] **Step 1: Write `docs/releases/v0.1.0.md`**

Sections, in this order:
  1. *What Portico 0.1.0 is*: three sentences from the README's opening,
     the independence notice in one line.
  2. *What is in it*: one bullet per area (public path, identity,
     sites, tenant arrival, branding, admin path, appliance image),
     each with its write-up number.
  3. *Requirements*: pc.7.6 or later; 9440 reachable only from the
     appliance and the management network; two public names; a
     certificate for them; Internet egress at the first boot.
  4. *Assets*: the four files of Task 2 and how to check them
     (`sha256sum -c`).
  5. *Upgrading from a development build*: in place with
     `install.sh --release 0.1.0` (Task 1's backup); a store written by
     0.1.0 names `portal_host`; a binary built before the rename (PR
     #36, development builds before 2026-10-25) does not read it, so
     going back below the rename means restoring the backup the
     upgrade took, not installing the old binary over the new store.
  6. *Known limits*: the list below, one bullet each, each pointing to
     its backlog line or write-up.
  7. *Measured on*: Prism Central pc.7.6.0.6 on two single-node AHV
     clusters, Keycloak 26.3, the appliance image 0.1.0-rc.1; no
     production load (write-up 29).

Known limits (from the backlog and write-up 29, wording to tighten, no
item added or dropped without a ruling):
  - the first boot pulls PostgreSQL and Keycloak from docker.io and
    quay.io: no identity provider without egress;
  - the public names cannot change after setup;
  - backup is a snapshot of the VM; there is no application-level
    export of Keycloak and no "new image and restore" path (decision 1);
  - tunnel mode not measured with a real Cloudflare tunnel;
  - tenant users are Keycloak users; no brokering to a customer's
    directory; browser only, no tenant API keys;
  - no adoption of existing projects or of an existing SAML identity
    provider (Portico refuses to overwrite one);
  - disabling a tenant does not end a Prism Central session already
    open;
  - a transient `401` or `504` from Prism Central's gateway can fail a
    tenant step: Retry on the tenant page completes it;
  - Disconnect does not count floating IPs and Flow security policies
    (the service account's role cannot read them);
  - every site uses the pc.7.6 API paths;
  - the pair of design 4.11 (documented in `docs/operating.md`, Sites):
    not measured with two appliances, and a maintenance flag reaches
    the other node only at its start.

- [ ] **Step 2: Check the file against the constraints**

Run: `grep -n -i -w 'lab' docs/releases/v0.1.0.md; grep -c 'write-up' docs/releases/v0.1.0.md`
Expected: no line with "lab" except a link into `docs/lab/`; at least
seven write-up references.

- [ ] **Step 3: Backlog**

Header paragraph: "The known limits of v0.1.0 (docs/releases/v0.1.0.md)
are drawn from this file." New line under "Deployment and lab
scripts": the application-level backup and restore of Keycloak's
database with the store, so a new image can take over an appliance's
state (decision 1).

- [ ] **Step 4: Commit**

```bash
git add docs/releases/v0.1.0.md docs/backlog.md
git commit -m "docs(release): the notes and known limits of v0.1.0"
```

### Task 4: The install guide for v0.1.0

**Files:**
- Modify: `docs/install.md` (rewrite)

**Interfaces:**
- Consumes: Task 1's backup line and directory; Task 3's notes path.

- [ ] **Step 1: Rewrite `docs/install.md`**

Keep its structure (What you need, What listens where, The appliance
image, An existing Rocky Linux 9 host, Updating) and change:
  - opening: "This guide installs Portico 0.1.0." and a link to the
    release notes; drop "Pre-release" and "the lab exercises".
  - names: `portal.msp.example` and `id.msp.example` everywhere
    (`prism.<msp>.example` is gone since the rename).
  - *What you need*, network: Internet egress from the appliance to
    `docker.io` and `quay.io` at the first boot (and `pkg.cloudflare.com`
    for tunnel mode on an existing host); NTP and DNS.
  - new section *Certificates*, between "What you need" and "What
    listens where": one certificate with both names in its SAN and
    `extendedKeyUsage = serverAuth`, served with its chain (leaf first,
    then intermediates) as the console's certificate file; a public CA
    or the MSP's own CA; with the MSP's own CA every browser that opens
    the portal must trust that CA (on macOS, "Always Trust" in the
    System keychain): a bare self-signed certificate is refused by
    Chrome and Safari even when installed (write-up 29). An `openssl`
    example that makes a CA and a leaf for the two names, in a fenced
    block, with no file path from the test environment.
  - step 5, the setup page, as the page is now: *Public names* (Portal
    public host, Identity public host), *Sites* with "Add site" (one
    step: address, the fingerprint to confirm, code, name, the
    one-time account; Portico creates the service account and the SAML
    trust), *Identity* with "Let Portico create the realm and clients",
    *Tenants*, then "Apply and restart". Read
    `internal/web/templates/setup.html` and `site_new.html` for the
    exact labels.
  - first-boot timing: the appliance is ready when the setup page's
    Identity card shows Keycloak up, about a minute after the network
    on a slow link; `healthz` alone says only that Portico answers.
  - *Updating*, replaced: take a snapshot of the VM in Prism Central;
    on the appliance download and check `install-<v>.tar.gz`, extract
    it, run `./deploy/install.sh --release <v>`; the installer stops
    Portico, copies the store and the key to
    `/var/lib/portico/backup-<old version>-<time>/`, replaces the
    binary and the files, restarts PostgreSQL and Keycloak only when
    their units changed; sessions end. To go back: restore the
    snapshot. The "new image, restore the directory" text goes.

- [ ] **Step 2: Check names and words**

Run: `grep -n 'prism\.<msp>\|Pre-release\|lab' docs/install.md`
Expected: nothing, except links into `lab/` or `docs/lab/` if any.

- [ ] **Step 3: Check every quoted UI label exists**

Run, for each label the guide puts in quotes or italics as on-screen
text: `grep -rlF '<label>' internal/web/templates internal/console`
Expected: every label found at least once. A label not found is fixed
in the guide, not in the UI.

- [ ] **Step 4: Commit**

```bash
git add docs/install.md
git commit -m "docs(install): the install guide of v0.1.0: certificates, first boot, upgrade"
```

### Task 5: README, operating guide and design for the release

**Files:**
- Modify: `README.md`, `docs/operating.md`,
  `docs/superpowers/specs/2026-10-17-portico-design.md` (§4.9, §9),
  `deploy/README.md` if it repeats the update path

- [ ] **Step 1: README**

  - status badge: `release-v0.1.0`, linked to the releases page;
  - the IMPORTANT box: replace the "Pre-release …" paragraph with the
    release: what 0.1.0 is for (trying Portico with a first tenant on
    an MSP's own Prism Central), the known limits link, "as is";
  - names `portal.msp.example` / `id.msp.example` in the table, the
    diagram and "How a request flows";
  - a short *Install* section after "Requirements": three lines and the
    link to `docs/install.md`;
  - the milestone table: rows 5 (backlog sweep, write-ups 27 and 28) and
    6 (general test and release, write-ups 29 and 30), "Later" gone;
  - "Status and validation": "two test clusters" in place of "one lab
    cluster"; the security section's "Lab defaults" sentence becomes
    "Before real use: replace the self-signed pairs with a certificate
    of your own (docs/install.md, Certificates) and set `tls_verify`
    on for each site";
  - Roadmap: step 1 done with 0.1.0; step 2 unchanged; add the
    container images in the qcow2 and the Keycloak backup.
  - the repository layout: add `docs/releases/`.

- [ ] **Step 2: Operating guide**

  - "Backup" (line 39): the snapshot of decision 1 first; the installer's
    copy for upgrades, where it is, that old ones stay until the MSP
    removes them; restoring that copy: stop portico, copy the files back
    with owner `portico` and their modes, start.
  - "Upgrading": `install.sh --release`, the backup; the dead reference
    to `lab/portico/deploy.sh` replaced by the install guide.

- [ ] **Step 3: Design**

  - §4.9 "Updates": the installer (`install.sh --release`) and its
    backup, not an rpm; the rpm stays out; the snapshot as the backup;
    the images pulled at first boot (already noted in write-up 29's PR,
    keep it).
  - §9: row 5 "done 2026-10-25 (write-ups 27 and 28)", row 6 "done
    <date> (write-ups 29, 30): tag `v0.1.0`" written in Task 8, not now.
  - ADR: none; decision 1 is recorded in §4.9 with its reason.

- [ ] **Step 4: Check**

Run: `grep -n 'prism\.<msp>\|Pre-release\|pre-release\|one lab\|Lab defaults' README.md docs/operating.md deploy/README.md`
Expected: nothing.

- [ ] **Step 5: Commit, push, PR**

```bash
git add README.md docs/operating.md docs/superpowers/specs/2026-10-17-portico-design.md deploy/README.md
git commit -m "docs: README, operating guide and design for v0.1.0"
```
Push `m6-release-docs`, open the PR (Tasks 3 to 5), green CI, ask
Marco for the merge.

---

## Release

### Task 6: Rehearsal tag `v0.1.0-rc.2`

Needs both PRs merged and Marco's OK for the tag push.

- [ ] **Step 1: Tag main and push the tag**

```bash
git checkout main && git pull --ff-only
git tag -a v0.1.0-rc.2 -m "Portico 0.1.0, release candidate 2"
git push origin v0.1.0-rc.2
```
Expected: `image.yml` starts on the tag; the notes check passes.

- [ ] **Step 2: Watch the run** (about 15 minutes; one background wait, not a poll loop)

Expected: both jobs green; a draft release `v0.1.0-rc.2`, marked
pre-release, its text the notes file, eight assets (four files, four
`.sha256`).

- [ ] **Step 3: Check the assets as an MSP would**

Download the draft's assets with `gh release download v0.1.0-rc.2`
into a fresh directory under `lab/appliance/artifact/`; run
`sha256sum -c ./*.sha256` there.
Expected: every file `OK`.

- [ ] **Step 4: Upgrade the appliance in place from the rehearsal tarball**

Lab write, announced in one line. The draft is not downloadable without
a token, so: copy `install-0.1.0-rc.2.tar.gz` and
`portico-0.1.0-rc.2-linux-amd64.tar.gz` to the appliance, extract,
run `sudo ./deploy/install.sh --binary ./portico`.
Expected: the installer's backup line with
`backup-dev-f37aafa-<time>`; `portico version` `0.1.0-rc.2`; settings
`running`; both sites available; `lab/sites/tenant-login.sh acme --site az-it-mi1`
and `--site az-us-nyc1` end on `200 https://msp.portico.test/console/`.

- [ ] **Step 5: Write-up 30, first part**

`docs/lab/2026-10-26-30-release.md` from `docs/lab/TEMPLATE.md`: the
rehearsal (run id, durations, asset sizes, checksums ok, the upgrade
and its backup, the logins). Inventory: the appliance's version.

- [ ] **Step 6: Ask Marco** to delete the rehearsal draft (the tag
stays) and for the `v0.1.0` tag. Fix anything the rehearsal found on a
new branch with its PR first; a fix means a new rehearsal tag
(`-rc.3`).

### Task 7: The tag `v0.1.0`

- [ ] **Step 1:** `git tag -a v0.1.0 -m "Portico 0.1.0"` on the main
  commit Marco approved; push the tag.
- [ ] **Step 2:** Watch the run; expected a draft `v0.1.0`, not
  pre-release, eight assets.
- [ ] **Step 3:** Tell Marco the draft's URL. He reads it and presses
  Publish himself.

### Task 8: After the publication

- [ ] **Step 1: The documented upgrade from the public release**

On the appliance, as the guide says: download and check
`install-0.1.0.tar.gz`, extract, `sudo ./deploy/install.sh --release 0.1.0`.
Expected: the binary fetched from the release and its checksum checked
by the installer; `portico version` `0.1.0`; the two logins as in
Task 6 step 4.

- [ ] **Step 2: Close the milestone**

Write-up 30 second part (the public download path, the published
assets' sizes); design §9 row 6 done with the date; README milestone
row 6; `portico-lab-state` memory. One branch `m6-close`, its PR,
Marco's OK for the merge.
