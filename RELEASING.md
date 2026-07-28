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
4. Create the public `polter-dev/scoop-bucket` repository on GitHub and initialize its
   default branch.
5. Create a fine-grained GitHub personal access token scoped to **only**
   `polter-dev/scoop-bucket`, with **Repository permissions > Contents: Read and write**,
   and store it in this repository as the Actions secret `SCOOP_BUCKET_GITHUB_TOKEN`.
6. Run the **Verify release secrets** workflow from the repository's **Actions** page
   before pushing a tag. It checks every cross-repository publishing token against its
   intended repository and confirms its Contents permission.

The v0.1.0 cask publish failed with `403: Resource not accessible by personal access
token` because its token was scoped to the wrong repository with zero repository
permissions. That configuration failure surfaces only at publish time, which is why the
pre-flight workflow exists — run it before every tag.

The workflow's built-in `GITHUB_TOKEN` creates the release in this repository. The
separate personal access tokens are required because the built-in token cannot push the
cask or Scoop manifest to another repository.

## Cut a release

From the commit to release, create and push a semantic-version tag:

```sh
git tag vX.Y.Z && git push origin vX.Y.Z
```

Tags such as `vX.Y.Z-rc.N` follow the same process and GoReleaser marks their GitHub
releases as pre-releases automatically.

GoReleaser creates the GitHub release as a draft because `.goreleaser.yaml` sets
`release.draft: true`. After the workflow succeeds, open the draft under the repository's
**Releases** page and review its notes and artifacts, including the generated `termp.rb`
and `termp.json`. The tag workflow does not update the public Homebrew tap or Scoop bucket.

Choose **Publish release** after approval. Publishing the release triggers a second workflow
jobs that verify the release is public, download the exact generated `termp.rb` and
`termp.json` from the release, and commit them to `polter-dev/homebrew-tap` and
`polter-dev/scoop-bucket`. If either job fails, the release remains public but that
package repository remains at its previous version, and the job can be rerun safely.

## Test locally without publishing

```sh
goreleaser release --snapshot --clean --skip=publish
```

Snapshot mode builds local artifacts without creating a GitHub release or updating the
Homebrew tap or Scoop bucket.
