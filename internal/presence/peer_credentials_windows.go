//go:build windows

package presence

import (
	"fmt"
	"net"

	"golang.org/x/sys/windows"
)

type pipeHandle interface {
	Fd() uintptr
}

func validatePipePeer(conn net.Conn) error {
	return validatePipePeerWithLookups(conn, currentProcessUserSID, namedPipeServerUserSID)
}

func validatePipePeerWithLookups(
	conn net.Conn,
	currentUser func() (*windows.SID, error),
	serverUser func(net.Conn) (*windows.SID, error),
) error {
	want, err := currentUser()
	if err != nil {
		return fmt.Errorf("presence: inspect current process user: %w", err)
	}
	got, err := serverUser(conn)
	if err != nil {
		return fmt.Errorf("presence: inspect named-pipe server user: %w", err)
	}
	return validatePeerSIDs(want, got)
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

func namedPipeServerUserSID(conn net.Conn) (*windows.SID, error) {
	handleConn, ok := conn.(pipeHandle)
	if !ok {
		return nil, fmt.Errorf("connection type %T does not expose a pipe handle", conn)
	}

	var processID uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(handleConn.Fd()), &processID); err != nil {
		return nil, fmt.Errorf("get named-pipe server process ID: %w", err)
	}

	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return nil, fmt.Errorf("open named-pipe server process %d: %w", processID, err)
	}
	defer windows.CloseHandle(process) //nolint:errcheck -- best-effort cleanup after inspection

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
