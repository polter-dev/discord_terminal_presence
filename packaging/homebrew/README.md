# Homebrew tap draft

No `termp` release exists yet (issue #9), so `termp.rb` contains placeholders and is
not installable as committed here.

At release time:

1. Replace `REPLACE_WITH_VERSION` with the version without its leading `v` (for
   example, use `1.2.3` for tag `v1.2.3`).
2. Copy the four SHA-256 values for the Darwin/Linux and amd64/arm64 archives from
   that GitHub release's `checksums.txt` into the matching placeholders.
3. Confirm every Worker URL redirects to its named GitHub release archive and
   every direct GitHub `mirror` downloads the same bytes.
4. Test the formula on the supported platforms before publishing it.

To publish the tap, create the `polter-dev/homebrew-tap` repository and copy the
completed formula to `Formula/termp.rb`. Users can then install it with:

```sh
brew tap polter-dev/tap
brew install termp
```

The primary URLs use `https://termp.polter.sh/dl/brew/{os}/{arch}` so successful
Homebrew fetches are counted by the Worker. The Worker redirects to the GitHub
release assets, and each formula stanza keeps the direct asset URL as its fallback
mirror; the archive bytes and SHA-256 remain identical.

A future submission to `homebrew-core` is a traction-gated follow-up and is out of
scope for this launch draft.
