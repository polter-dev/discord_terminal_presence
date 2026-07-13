//go:build !windows

package presence

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func dialDiscordIPC() (net.Conn, error) {
	envNames := []string{"XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP"}
	dirs := make([]string, 0, len(envNames)+1)
	for _, name := range envNames {
		if dir := os.Getenv(name); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	dirs = append(dirs, "/tmp")

	var failures strings.Builder
	for _, dir := range dirs {
		for i := 0; i <= 9; i++ {
			path := filepath.Join(dir, fmt.Sprintf("discord-ipc-%d", i))
			conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
			if err == nil {
				return conn, nil
			}
			fmt.Fprintf(&failures, "  %s: %v\n", path, err)
		}
	}

	return nil, fmt.Errorf("presence: no Discord IPC socket accepted a connection:\n%s", failures.String())
}
