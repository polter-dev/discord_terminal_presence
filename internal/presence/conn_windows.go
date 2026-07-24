//go:build windows

package presence

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
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
	endpointFound := false
	for _, path := range discordIPCPipeCandidates(override) {
		if discordIPCPipeExists(path) {
			endpointFound = true
		}
		timeout := 500 * time.Millisecond
		conn, err := dial(path, &timeout)
		if err != nil {
			fmt.Fprintf(&failures, "  %s: %v\n", path, err)
			continue
		}
		endpointFound = true
		if err := verify(conn); err != nil {
			_ = conn.Close()
			fmt.Fprintf(&failures, "  %s: %v\n", path, err)
			continue
		}
		return conn, nil
	}

	if !endpointFound {
		return nil, fmt.Errorf("%w:\n%s", ErrDiscordIPCNotFound, failures.String())
	}
	return nil, fmt.Errorf("%w:\n%s", ErrDiscordIPCUnreachable, failures.String())
}

var waitNamedPipeW = windows.NewLazySystemDLL("kernel32.dll").NewProc("WaitNamedPipeW")

func discordIPCPipeExists(path string) bool {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	ok, _, callErr := waitNamedPipeW.Call(uintptr(unsafe.Pointer(pathPtr)), 0)
	if ok != 0 {
		return true
	}
	switch callErr {
	case windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND:
		return false
	default:
		return true
	}
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
