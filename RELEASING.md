# Releasing

Releases are automated with [release-please](https://github.com/googleapis/release-please-action).
You do not create a release branch, edit `CHANGELOG.md`, or run `git tag` for a normal release.

## One-time setup: GitHub App

The workflow needs a short-lived GitHub App installation token instead of a long-lived PAT.
Create the App under the **`aiyor` organization**:

1. Open `https://github.com/organizations/aiyor/settings/apps/new`.
2. Set:
   - **GitHub App name:** `lxm-release-bot` (or another globally unique name)
   - **Homepage URL:** `https://github.com/aiyor/lxm`
   - **Webhook:** inactive (not needed)
   - **Repository permissions:**
     - `Contents`: Read and write
     - `Pull requests`: Read and write
     - `Metadata`: Read-only (mandatory)
     - Leave Workflows, Actions, Secrets, Administration, and Members at **No access**.
   - **Where can this GitHub App be installed?** Only on this account.
3. Create the App and record its **App ID**.
4. Under **Private keys**, generate a private key and download the `.pem` file. Treat this file as
   a high-value secret; it is shown only once.
5. Install the App on the organization and select **only `aiyor/lxm`**.

### Store the App credentials

In the repository, create an environment named **`release`**
(Settings → Environments → New environment):

- Deployment branches: **Selected branches** → `main`
- No required reviewers (otherwise every push to `main` blocks automation)
- Add environment secret **`RELEASE_APP_PRIVATE_KEY`** containing the full PEM, including the
  `BEGIN`/`END` lines.
- Add repository variable **`RELEASE_APP_ID`** containing the App ID.

Environment secrets are only readable by jobs that declare `environment: release` and run from an
allowed branch (`main`). The App private key is never needed on feature branches or PRs.

Remove any old `RELEASE_PLEASE_TOKEN` repository secret and revoke the PAT once the App flow works.

## Normal release flow

1. Merge feature/fix PRs to `main` as usual.
2. The **Release Please** workflow uses the App token to open or update a PR titled
   `chore(main): release X.Y.Z` containing:
   - the next version in `.release-please-manifest.json`
   - a generated `CHANGELOG.md` section
3. Review that PR. Merge it when you want to release.
4. Merging creates the `vX.Y.Z` tag and GitHub release.
5. The tag triggers the existing `.github/workflows/release.yml`, which runs GoReleaser and
   uploads the binaries/checksums.

release-please creates the GitHub release body; GoReleaser's default `keep-existing` release-notes
mode preserves it while uploading artifacts.

## Versioning policy

Version increments follow [Conventional Commits](https://www.conventionalcommits.org/) from the
squash-merge titles on `main`:

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

Use this sparingly; the normal path is to choose the correct conventional-commit type when
squash-merging. You control **what** is released (release PR contents) and **when** (it is only
tagged when you merge the release PR).

## Private-key rotation

If the private key is exposed or you want to rotate it:

1. Generate a new private key for the App.
2. Update the `RELEASE_APP_PRIVATE_KEY` environment secret.
3. Delete the old private key from the App settings.

Existing installation tokens expire within about an hour.

## Manual fallback

If release-please is unavailable, the old path still works: merge a manual changelog change, then
create and push an annotated tag:

```bash
git switch main
git pull --ff-only origin main
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

The tag triggers `.github/workflows/release.yml` directly. Prefer the automated flow so the tag
and changelog stay in sync.
