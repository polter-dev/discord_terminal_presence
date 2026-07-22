# Releasing

## Prerequisites

1. Create the public `polter-dev/homebrew-tap` repository on GitHub. Initialize it with a
   default branch (for example, by adding a README) so GoReleaser can clone and update it.
2. Create a fine-grained GitHub personal access token that has access to only
   `polter-dev/homebrew-tap` and grants **Repository permissions > Contents: Read and
   write**. The token owner must have write access to the tap repository.
3. In `polter-dev/discord_terminal_presence`, open **Settings > Secrets and variables >
   Actions**, create a repository secret named `HOMEBREW_TAP_GITHUB_TOKEN`, and paste the
   token value.

The workflow's built-in `GITHUB_TOKEN` creates the release in this repository. The
separate personal access token is required because the built-in token cannot push the
cask to another repository.

## Cut a release

From the commit to release, create and push a semantic-version tag:

```sh
git tag vX.Y.Z && git push origin vX.Y.Z
```

Tags such as `vX.Y.Z-rc.N` follow the same process and GoReleaser marks their GitHub
releases as pre-releases automatically.

GoReleaser creates the GitHub release as a draft because `.goreleaser.yaml` sets
`release.draft: true`. After the workflow succeeds, open the draft under the repository's
**Releases** page, review its notes and artifacts, then choose **Publish release**. Confirm
that `checksums.txt.sigstore.json` is present before publishing.

## Artifact signature identity

The release workflow signs `checksums.txt` with Sigstore keyless signing. For a release
tag `vX.Y.Z`, the exact expected certificate identity is:

```text
https://github.com/polter-dev/discord_terminal_presence/.github/workflows/release.yml@refs/tags/vX.Y.Z
```

The expected OIDC issuer is `https://token.actions.githubusercontent.com`. The installer
constructs the identity from the exact release tag, verifies `checksums.txt` against the
Sigstore bundle and this identity/issuer pair, and only then verifies the archive digest.
Confirm this identity remains correct for the repository and workflow before GA.

## Test locally without publishing

```sh
goreleaser release --snapshot --clean --skip=publish,sign
```

Snapshot mode builds local artifacts without creating a GitHub release or updating the
Homebrew tap. Signing is skipped because local snapshot builds do not have the release
workflow's GitHub OIDC identity.
