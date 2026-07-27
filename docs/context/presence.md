# Presence context

`StatusProbe` uses the caller's context throughout Discord IPC endpoint
discovery, dialing, and handshake frame I/O. Cancellation interrupts an active
socket or named-pipe dial and forces blocked frame reads or writes to return
promptly. Caller deadlines bound each phase without replacing the existing
aggregate discovery budgets or the two-second status I/O timeout.

The daemon's regular `RichClient` continues to use its independent default I/O
timeout.
