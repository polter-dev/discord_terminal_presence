//go:build linux

package presence

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func validatePeerCredentials(conn net.Conn, euid int) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("presence: cannot validate peer credentials for connection type %T", conn)
	}

	rawConn, err := unixConn.SyscallConn()
	if err != nil {
		return fmt.Errorf("presence: access Unix socket descriptor: %w", err)
	}

	var peerErr error
	if err := rawConn.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			peerErr = fmt.Errorf("presence: get peer credentials: %w", err)
			return
		}
		if int(cred.Uid) != euid {
			peerErr = fmt.Errorf("presence: peer UID %d does not match effective UID %d", cred.Uid, euid)
		}
	}); err != nil {
		return fmt.Errorf("presence: inspect Unix socket descriptor: %w", err)
	}
	return peerErr
}
