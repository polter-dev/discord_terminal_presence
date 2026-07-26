# Homebrew tap draft

No `termp` release exists yet (issue #9), so `termp.rb` contains placeholders and is
not installable as committed here.

At release time:

1. Replace `REPLACE_WITH_VERSION` with the version without its leading `v` (for
   example, use `1.2.3` for tag `v1.2.3`).
2. Copy the four SHA-256 values for the Darwin/Linux and amd64/arm64 archives from
   that GitHub release's `checksums.txt` into the matching placeholders.
3. Confirm every formula URL downloads its named archive directly from
   `github.com/polter-dev/discord_terminal_presence/releases/download/...`.
4. Test the formula on the supported platforms before publishing it.

To publish the tap, create the `polter-dev/homebrew-tap` repository and copy the
completed formula to `Formula/termp.rb`. Users can then install it with:

```sh
brew tap polter-dev/tap
brew install termp
```

Because the formula URLs point directly to assets on the `termp` GitHub release,
successful Homebrew fetches contribute to GitHub's per-asset `download_count`.
Do not mirror or move these archives to a separate download host.

A future submission to `homebrew-core` is a traction-gated follow-up and is out of
scope for this launch draft.
