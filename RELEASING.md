# Releasing

Releases are automated with [release-please](https://github.com/googleapis/release-please-action).
You do not create a release branch, edit `CHANGELOG.md`, or run `git tag` for a normal release.

## One-time setup

Create a **fine-grained personal access token** for this repository with:

- **Contents:** read and write
- **Pull requests:** read and write

Add it as the repository secret **`RELEASE_PLEASE_TOKEN`** (Settings → Secrets and
variables → Actions → New repository secret).

A PAT is required instead of the default `GITHUB_TOKEN`: PRs and tags created with
`GITHUB_TOKEN` do not trigger other workflows, so the release PR would not run CI and the
tag would not trigger `.github/workflows/release.yml` (GoReleaser). The release-please
workflow fails loudly if the secret is missing.

## Normal release flow

1. Merge feature/fix PRs to `main` as usual.
2. The **Release Please** workflow opens or updates a PR titled
   `chore(main): release X.Y.Z` containing:
   - the next version in `.release-please-manifest.json`
   - a generated `CHANGELOG.md` section
3. Review that PR. Merge it when you want to release.
4. Merging creates the `vX.Y.Z` tag and GitHub release.
5. The tag triggers the existing `.github/workflows/release.yml`, which runs GoReleaser
   and uploads the binaries/checksums to the release.

release-please creates the GitHub release body; GoReleaser's default `keep-existing`
release-notes mode preserves it while uploading artifacts.

## Versioning policy

Version increments follow [Conventional Commits](https://www.conventionalcommits.org/)
from the squash-merge titles on `main`:

| Commit | Bump |
| :-- | :-- |
| `fix: ...` | patch |
| `feat: ...` | minor |
| `feat!: ...` or `BREAKING CHANGE:` footer | major |

To override the computed version, add a `Release-As:` footer to a commit message:

```text
feat(network): host access and external ingress grants (#15)

...details...

Release-As: 1.6.1
```

Use this sparingly; the normal path is to choose the correct conventional-commit type
when squash-merging. You control **what** is released (release PR contents) and **when**
(it is only tagged when you merge the release PR).

## Manual fallback

If release-please is unavailable, the old path still works: merge a manual changelog
change, then create and push an annotated tag:

```bash
git switch main
git pull --ff-only origin main
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

The tag triggers `.github/workflows/release.yml` directly. Prefer the automated flow so
the tag and changelog stay in sync.
