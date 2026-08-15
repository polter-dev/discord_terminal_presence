//go:build windows

package presence

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type pipeHandle interface {
	Fd() uintptr
}

func validatePipePeer(conn net.Conn) error {
	return validatePipePeerWithLookups(
		conn,
		currentProcessUserSID,
		openNamedPipeServerProcess,
		namedPipeServerUserSID,
		namedPipeServerImageName,
		discordIPCPathOverrideSet(os.Getenv("DISCORD_IPC_PATH")),
	)
}

func discordIPCPathOverrideSet(path string) bool {
	return filepath.IsAbs(path)
}

// validatePipePeerWithLookups opens the named-pipe server process exactly
// once and passes the resulting handle to both the SID check and the image
// name check. Opening it twice (one call per check) let the two checks
// resolve the server PID independently: if the server process exited
// between the two opens and Windows reused the PID, the image-name check
// could validate a different process than the SID check approved (#572).
// A single handle removes that desync and halves the syscalls. The SID
// check remains the actual trust boundary and the image check remains
// deliberately fail-open on lookup failure (verifyDiscordServerImage);
// this only changes handle lifetime, not either check's semantics.
func validatePipePeerWithLookups(
	conn net.Conn,
	currentUser func() (*windows.SID, error),
	openServerProcess func(net.Conn) (windows.Handle, error),
	serverUser func(windows.Handle) (*windows.SID, error),
	serverImageName func(windows.Handle) (string, error),
	overrideSet bool,
) error {
	want, err := currentUser()
	if err != nil {
		return fmt.Errorf("presence: inspect current process user: %w", err)
	}
	process, err := openServerProcess(conn)
	if err != nil {
		return fmt.Errorf("presence: open named-pipe server process: %w", err)
	}
	defer windows.CloseHandle(process) //nolint:errcheck -- best-effort cleanup after inspection

	got, err := serverUser(process)
	if err != nil {
		return fmt.Errorf("presence: inspect named-pipe server user: %w", err)
	}
	if err := validatePeerSIDs(want, got); err != nil {
		return err
	}
	if overrideSet {
		return nil
	}
	imageName, imageErr := serverImageName(process)
	return verifyDiscordServerImage(imageName, false, imageErr)
}

func validatePeerSIDs(current, server *windows.SID) error {
	if current == nil || server == nil {
		return fmt.Errorf("presence: cannot validate named-pipe server user SID")
	}
	if !windows.EqualSid(current, server) {
		return fmt.Errorf("presence: named-pipe server user SID does not match current process user SID")
	}
	return nil
}

func currentProcessUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get current process token user: %w", err)
	}
	return user.User.Sid, nil
}

func namedPipeServerUserSID(process windows.Handle) (*windows.SID, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return nil, fmt.Errorf("open named-pipe server process token: %w", err)
	}
	defer token.Close() //nolint:errcheck -- best-effort cleanup after inspection

	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get named-pipe server token user: %w", err)
	}
	return user.User.Sid, nil
}

func namedPipeServerImageName(process windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(process, 0, &buf[0], &size); err != nil {
		return "", fmt.Errorf("query named-pipe server process image name: %w", err)
	}
	return windows.UTF16ToString(buf[:size]), nil
}

func openNamedPipeServerProcess(conn net.Conn) (windows.Handle, error) {
	handleConn, ok := conn.(pipeHandle)
	if !ok {
		return 0, fmt.Errorf("connection type %T does not expose a pipe handle", conn)
	}

	var processID uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(handleConn.Fd()), &processID); err != nil {
		return 0, fmt.Errorf("get named-pipe server process ID: %w", err)
	}

	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return 0, fmt.Errorf("open named-pipe server process %d: %w", processID, err)
	}
	return process, nil
}
