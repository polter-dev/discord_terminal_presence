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
4. Run the **Verify release secrets** workflow from the repository's **Actions** page
   before pushing a tag. Token scope failures otherwise surface only at publish time,
   which caused v0.1.0 to be published broken.

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
**Releases** page and review its notes and artifacts, including the generated `termp.rb`.
The tag workflow does not update the public Homebrew tap.

Choose **Publish release** after approval. Publishing the release triggers a second workflow
job that verifies the release is public, downloads the exact generated `termp.rb` from the
release, and commits it to `polter-dev/homebrew-tap`. If this job fails, the release remains
public but the tap remains at its previous version and the job can be rerun safely.

## Test locally without publishing

```sh
goreleaser release --snapshot --clean --skip=publish
```

Snapshot mode builds local artifacts without creating a GitHub release or updating the
Homebrew tap.
