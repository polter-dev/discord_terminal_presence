# Autostart service identity

Windows uses the stable scheduled-task name `\Terminal Presence\termp`. Keeping
one well-known task preserves existing autostart registrations during upgrades
and avoids leaving obsolete per-installation tasks behind.

The task definition's executable command is its ownership check. Status counts
the task as this installation's autostart only when that command matches the
running executable (case-insensitively, as Windows paths are). If the stable
task targets another executable, status reports that conflict explicitly and
install, uninstall, enable, and disable refuse to modify it by default.
`autostart install --force` deliberately replaces a foreign task, while
`autostart uninstall --force` deliberately removes one. Reinstalling from the
same executable still replaces the definition, preserving the reconciliation
behavior used to apply updated service settings.
