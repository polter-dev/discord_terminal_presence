# Detector context

The detector matches running processes to registered tools, then chooses one
featured tool for Discord Rich Presence. Other detected tools are exposed in
`Detection.Others` and rendered in the card's `also:` state line.

Presence and featured eligibility differ on Windows. Losing foreground starts
the terminal idle clock; the window's last foreground time is retained across
scans, and `idle_clear_timeout` controls the resulting grace period. While the
window is focused, recent process CPU activity is sufficient for featured
eligibility even without recent system input. While it is unfocused, CPU
activity only corroborates the retained focus clock and cannot extend the
grace period. A matched process that is still attached to a resolved terminal
remains eligible for `Others`, however, because an unfocused terminal window
is still a legitimate member of the running collection. Processes with a
definitive lack of a terminal and detached tmux processes are excluded from
both sets.

On macOS and Linux, collection membership continues to use the same terminal
activity eligibility as featured selection.

Process-list failures retain the last presence for one or two consecutive
scans. On the third consecutive failure, the detector emits `None` immediately
so the writer clears stale Discord presence. A successful scan resets the
failure counter, and normal detection debounce applies when presence recovers.
