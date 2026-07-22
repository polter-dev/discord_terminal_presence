//go:build windows

package presence

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

func dialDiscordIPC() (net.Conn, error) {
	return dialDiscordIPCWith(winio.DialPipe, validatePipePeer)
}

type dialPipeFunc func(string, *time.Duration) (net.Conn, error)

func dialDiscordIPCWith(dial dialPipeFunc, verify func(net.Conn) error) (net.Conn, error) {
	var failures strings.Builder
	for i := 0; i <= 9; i++ {
		path := fmt.Sprintf(`\\.\pipe\discord-ipc-%d`, i)
		timeout := 500 * time.Millisecond
		conn, err := dial(path, &timeout)
		if err != nil {
			fmt.Fprintf(&failures, "  %s: %v\n", path, err)
			continue
		}
		if err := verify(conn); err != nil {
			_ = conn.Close()
			fmt.Fprintf(&failures, "  %s: %v\n", path, err)
			continue
		}
		return conn, nil
	}

	return nil, fmt.Errorf("presence: no Discord IPC pipe accepted a connection:\n%s", failures.String())
}
