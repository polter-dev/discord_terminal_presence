//go:build windows

package presence

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

// Windows uses go-winio to dial named pipes at \\.\pipe\discord-ipc-0..9 for Discord IPC.
func dialDiscordIPC() (net.Conn, error) {
	var failures strings.Builder
	for i := 0; i <= 9; i++ {
		path := fmt.Sprintf(`\\.\pipe\discord-ipc-%d`, i)
		timeout := 500 * time.Millisecond
		conn, err := winio.DialPipe(path, &timeout)
		if err == nil {
			return conn, nil
		}
		fmt.Fprintf(&failures, "  %s: %v\n", path, err)
	}

	return nil, fmt.Errorf("presence: no Discord IPC pipe accepted a connection:\n%s", failures.String())
}
