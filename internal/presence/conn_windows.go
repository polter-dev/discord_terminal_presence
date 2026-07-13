//go:build windows

package presence

import (
	"errors"
	"net"
)

// TODO(windows): implemented in a follow-up PR using gopkg.in/natefinch/npipe.v2
// to dial \\.\pipe\discord-ipc-0..9. Do not remove this seam.
func dialDiscordIPC() (net.Conn, error) {
	return nil, errors.New("presence: windows named-pipe IPC not yet implemented")
}
