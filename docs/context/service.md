# service (package `internal/service`)

**Purpose:** Installs, reconciles, enables, disables, removes, and reports the per-OS
login service for the current termp executable.

**Public surface:** `Manager` exposes install/definition install, uninstall, enable,
disable, and context-bounded status. `State` describes support, ownership, loaded/enabled
state, paths, and conflicts. Builders render launchd, systemd, and Windows task payloads.

**Key files:** `internal/service/service.go` contains shared management and launchd/
systemd definitions. `windows.go` contains scheduled-task identity and XML. The
Windows autostart companion launcher lives in `cmd/termpw` (`-H=windowsgui`).

**Invariants / gotchas:** Platform status calls honor their context bound. Installed
definitions are owned by their executable command; foreign definitions are not modified
without force.

Install snapshots the prior platform definition and activation state before replacing it.
If post-write activation fails, macOS and Linux restore the prior file or remove a new one,
then restore the prior loaded/enabled state; Windows restores or deletes the scheduled task
and restarts it only when it was running before the attempt. Rollback failures are joined
with the original activation error so a reported failure does not hide incomplete cleanup
(#519).
An activating macOS or Linux install still replaces an existing definition when its prior
loaded or enabled state cannot be determined. If activation fails, rollback restores the
previous definition but leaves it inactive when the prior activation state is unknown, and
the install error names the affected service plus the native status command to check it.
Only states known to have been active or enabled are restored during rollback, and
reactivation failures are joined into the install error so incomplete rollback is visible
(#527).
Linux enablement and activation are two independent dimensions, so an unknown reading of
one of them is reported and left alone without discarding the other one: a unit known to
have been active is still restarted, and a unit known to have been enabled is still
re-enabled. The unknown-state warning is raised only when rollback actually touched
activation, so the `daemon-reload` failure path stays silent about it. macOS and Linux
also skip reactivation when the failed replacement could not be stopped, matching the
Windows guard, because the OS may still be running it. When macOS rollback cannot unload
a replacement it installed over nothing, the definition is kept and the error says so,
matching `Uninstall`, so the job can still be booted out on a retry (#539).
Windows snapshot queries are best effort so an export or verbose-state query failure does
not block an overwrite that Task Scheduler would accept. If activation then fails without
a complete snapshot, the replacement definition is left in place; a pre-existing task is
never deleted or otherwise rolled back when its prior definition could not be captured;
that outcome now returns a warning naming the task and the query that shows what is
installed, instead of reporting a silent success (#539).
When Windows rollback cannot end the replacement task, a valid verbose status showing the
task is stopped makes the failure benign. A running or indeterminate result is joined into
the install error, and prior activation is not restarted while the replacement process may
still be running (#527).
Filesystem-backed launchd and systemd tests run only on their native host OS because their
temporary home isolation follows Unix home-directory semantics. Simulated Windows rollback
tests do not depend on a home directory and continue to run on Windows CI.

Executable resolution and install validation errors name both the failed operation and
the path being checked, so platform path errors retain actionable termp context.
Forced install still makes the supplied path absolute, but deliberately bypasses
symlink resolution and unstable-path rejection.

`ValidateInstallExecutable` resolves symlinks only to feed the unstable-path
heuristic, so it separates that resolution's two failure modes (#472). A path
that does not exist is still refused, but with a message that names the path and
the next command to run, because on Windows the underlying error is a bare
`syscall.Errno` that renders as `The system cannot find the path specified.` with
no path in it at all. Any other resolution failure (an unreadable parent
directory, an unfollowable reparse point, a flaky share) no longer aborts the
install: validation falls back to judging the unresolved absolute path, which
still catches temp-directory and source-tree installs. Turning a working install
into a hard failure over a check that is only advisory was the actual defect.
`cmd/termp` covers the wiring directly, so deleting the validation call from
`install()` now fails the suite instead of passing it.

Note that `termp setup` still resolves the executable without calling
`ValidateInstallExecutable`, so it can register a path that `autostart install`
would refuse. That divergence is deliberate and unresolved: adding the check to
setup would introduce a new way for a working setup to fail, and dropping it from
install would remove the temp-directory guard.

Windows uses the stable scheduled-task name `\Terminal Presence\termp`. Keeping
one well-known task preserves existing autostart registrations during upgrades
and avoids leaving obsolete per-installation tasks behind.

The logon task runs the Windows-only companion launcher `termpw.exe`
(`cmd/termpw`, linked `-H=windowsgui`) rather than `termp.exe start
--foreground` (issue #473). The old console-subsystem daemon kept a console
window open for the daemon's whole life under `InteractiveToken`; the launcher
has no console of its own, spawns `termp.exe start --foreground` with
`CREATE_NO_WINDOW`, waits for the daemon's lifetime so Task Scheduler still owns
the process (`RestartOnFailure` and `schtasks /End` keep working), and
propagates the daemon's exit status so a real crash still triggers restart.
`windowsTaskExec` selects the launcher when it sits beside the daemon (the
shipped layout: goreleaser ships `termpw.exe` in the Windows `.zip` and Scoop
archives next to `termp.exe`). A Scoop invocation path under `shims` is special:
the sibling `shims\termpw.exe` is itself a console shim, so task generation bypasses it
and targets the real GUI launcher at `apps\termp\current\termpw.exe` (#510). It keeps
accepting the old shim launcher as owned so reinstall can reconcile it in place. The
launcher lookup fails closed when a Scoop shim has no real companion rather than
registering either console shim. Non-Scoop installs still fall back to `termp.exe start
--foreground` when the launcher is absent (a hand-assembled install), trading the fix for
a working autostart. `BuildWindowsTaskXML` takes an explicit command and
arguments and omits `<Arguments>` when empty (the launcher takes none).

The task definition's executable command is its ownership check. Because the
task now points at `termpw.exe` while the running binary is `termp.exe`,
`ownsWindowsTaskCommand` treats a task command as owned when it is either the
running executable itself (tasks written before the launcher existed, and the
fallback path — so upgrades reconcile in place rather than orphan/duplicate),
the sibling `termpw.exe` in the same install directory, or the mapped Scoop
`apps\termp\current\termpw.exe` launcher. A `termpw.exe` in any unrelated directory
is still foreign. Without this the Status ownership check would
report our own launcher task as foreign and lock install/uninstall/enable/
disable behind `--force`. Windows expands
percent-style environment variables and normalizes quotes, separators, dot
segments, and trailing separators, then compares paths case-insensitively. It
expands environment variables only in the raw task command, not in resolved
filesystem paths, and clamps parent traversal at a drive root. On Windows it
then opens both paths and compares volume/file identity, which recognizes 8.3
names, junctions, and drive-substituted aliases. Junction/symlink resolution
(`GetFinalPathNameByHandle`) is confined to this ownership-identity comparison.
New task definitions instead persist the stable invocation path exactly as it
was resolved for install (a Scoop `current` junction or shim, a Homebrew or
hand-placed path); the write path deliberately does **not** junction-follow,
because canonicalizing through Scoop's `apps\termp\current` junction would bake a
versioned `apps\termp\<version>` directory into the task that `scoop update`
later deletes, silently killing autostart (issue #502). The companion-launcher
probe (`windowsTaskExec`) likewise keeps the launcher command on the stable
`current` path. If the stable task targets another
executable, status reports that conflict explicitly and install, uninstall,
enable, and disable refuse to modify it by default.
`autostart install --force` deliberately replaces a foreign task, while
`autostart uninstall --force` deliberately removes one. Reinstalling from the
same executable still replaces the definition, preserving the reconciliation
behavior used to apply updated service settings.

Linux and macOS apply the same ownership contract to the executable parsed from
the systemd unit's `ExecStart` or the launchd plist's first `ProgramArguments`
entry. Unix paths are cleaned and existing symlinks are resolved before
comparison. Status surfaces a definition targeting a different absolute
executable as foreign; non-forced mutations refuse it, while forced install and
uninstall may take it over or remove it. Unparseable, dynamic, or non-absolute
targets remain fail-open rather than risking a parser gap blocking legitimate
users from reconciling termp's own definitions. A definition that cannot be read
is different: its ownership cannot be verified, so non-forced mutations refuse it
with an actionable `--force` message, while forced install and uninstall proceed.
The macOS launch agent runs `start --foreground --internal-daemon-log` and sends
launchd-managed stdout/stderr to `/dev/null`; the daemon opens and rotates
`~/Library/Logs/termp.log` itself, preventing launchd from retaining a renamed inode.
Linux continues to leave output ownership to journald.

The Windows task retries a failed daemon up to three times at one-minute
intervals. Disable and uninstall treat `schtasks /End` as required: disable
surfaces an end failure, and uninstall refuses to delete the task definition
unless the running task was ended successfully.

Windows lifecycle decisions do not parse localized `schtasks` prose. Presence is
checked first with a fast targeted headerless CSV query, whose command exit
status distinguishes presence from absence. Full enumeration is reserved for
failures where the targeted command did not exit normally; tolerant CSV parsing
still accepts usable task rows when enumeration also exits nonzero. If `/Run`
fails, the fixed "Last Run Result" column (index 6) of the verbose CSV is read
for the numeric Task Scheduler result `0x41301` (`SCHED_S_TASK_RUNNING`) to
identify an already-running task; only that column is checked, so a coincidental
sentinel value in an unrelated numeric column cannot false-positive (issue #504).
A presence query identifies a concurrently removed task.
Synthetic Japanese and German CSV fixtures run on every OS. Separate realistic
fixtures cover variable-width/error rows and a 30-column verbose row containing
an embedded quoted executable command. The Windows integration test installs
through a real directory junction, an available 8.3 short name, and an
available substituted drive.

**Depends on / used by:** Uses OS service commands through an injectable runner; used by
CLI autostart, setup reconciliation, and status.

**Open questions / TODO:** Confirm the Windows lifecycle on non-English real
hardware and ownership through real 8.3, junction, and substituted-drive paths;
CI coverage is not a substitute for the hardware verification tracked with
Windows testing issue #275.
