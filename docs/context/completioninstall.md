# completioninstall (package `internal/completioninstall`)

**Purpose:** Installs and removes generated termp shell-completion files without editing
shell startup files.

**Public surface:** `DetectShell` chooses bash, zsh, or fish with bash as the fallback.
`TargetPath`, `Install`, and `Uninstall` manage the one owned path per shell.
`UninstallAll` removes all supported-shell paths. `Note` returns optional activation
guidance.

**Key files:** `internal/completioninstall/completioninstall.go` contains shell detection,
path resolution, atomic install, uninstall, and activation notes.

**Invariants / gotchas:** Install refuses to replace non-regular files and writes through
a temporary file plus rename. Missing files are successful no-ops on uninstall.
`UninstallAll` always attempts bash, zsh, and fish; it returns every successfully removed
path and an `errors.Join` aggregate labeled by shell for all failures.

**Depends on / used by:** Standard library only. `cmd/termp` uses it for setup and
`termp completion uninstall`.

**Open questions / TODO:** None currently.
