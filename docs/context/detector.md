# Detector context

The detector matches running processes to registered tools, then chooses one
featured tool for Discord Rich Presence. Other detected tools are exposed in
`Detection.Others` and rendered in the card's `also:` state line.

Presence and featured eligibility differ on Windows. Foreground-window and CPU
corroboration prevent a tool in an inactive terminal window from becoming the
featured tool. A matched process that is still attached to a resolved terminal
remains eligible for `Others`, however, because an unfocused terminal window is
still a legitimate member of the running collection. Processes with a
definitive lack of a terminal and detached tmux processes are excluded from
both sets.

On macOS and Linux, collection membership continues to use the same terminal
activity eligibility as featured selection.
