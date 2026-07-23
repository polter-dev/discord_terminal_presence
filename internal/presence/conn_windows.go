//go:build windows

package presence

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

func dialDiscordIPC() (net.Conn, error) {
	return dialDiscordIPCWith(os.Getenv("DISCORD_IPC_PATH"), winio.DialPipe, validatePipePeer)
}

type dialPipeFunc func(string, *time.Duration) (net.Conn, error)

func dialDiscordIPCWith(override string, dial dialPipeFunc, verify func(net.Conn) error) (net.Conn, error) {
	var failures strings.Builder
	if override != "" && !filepath.IsAbs(override) {
		fmt.Fprintf(&failures, "  DISCORD_IPC_PATH %q is not absolute; override ignored\n", override)
	}
	for _, path := range discordIPCPipeCandidates(override) {
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

func discordIPCPipeCandidates(override string) []string {
	paths := make([]string, 0, 11)
	seen := make(map[string]struct{}, 11)
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	if filepath.IsAbs(override) {
		add(override)
	}
	for i := 0; i <= 9; i++ {
		add(fmt.Sprintf(`\\.\pipe\discord-ipc-%d`, i))
	}
	return paths
}
