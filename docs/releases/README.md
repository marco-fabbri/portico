# Releasing Portico

A release is an annotated tag on `main`; the image workflow builds the
appliance image, the edge image, their OpenSCAP reports and the binary
tarball, and makes a **draft** release with the notes as its body
(write-up 30). Marco reads the draft and publishes it. The tag holds
everything the release says about itself, so the documentation lands on
`main` before the tag, never after: a tag already pushed is not moved
(0.3.0 was tagged with its README behind; the draft did not exist yet,
so the run was cancelled, the tag deleted and made again on the
documented `main`, the one exception this rule allows).

Before the tag, on `main`, with no open pull request of the release:

1. **Release notes** `docs/releases/v<version>.md`: what changed, what
   was fixed, what the MSP gives, the known limits, with a link to the
   earlier limits that still apply.
2. **README**: the release badge and the "Current release" box with its
   known-limits link (CI checks both against the newest notes), the
   install paragraph's link to the notes, the "What it does" table when
   a piece is new, the diagram when the picture changed, the "Status
   and validation" row, the "Documentation" counts, the roadmap.
3. **Design document** `docs/superpowers/specs/2026-10-17-portico-design.md`,
   section 9: the milestone row, with its write-ups, design and ADRs.
4. **Install guide** and **operating guide** for whatever the release
   changes in how the appliance is installed, upgraded or run, and
   `docs/api.md` when the JSON API changed.
5. **The lab's write-ups and inventory**, outside the repository,
   updated with the acceptance.
6. **A manual run of the image workflow** on `main`
   (`gh workflow run image.yml --ref main -f version=<version>-dev`):
   both images build and pass their boot tests. The `release` job runs
   on tags only.
7. The suite green on `main` (`go test ./...`, `scripts/check-repo.sh`).

Then the tag, with Marco's go:

```
git tag -a v<version> -m "Portico <version>: <one line>" <main commit>
git push origin v<version>
```

After the draft is published: the lab appliance upgraded in place to
the release (`docs/install.md`, Updating) and the lab inventory, outside
the repository, says which version runs where.
