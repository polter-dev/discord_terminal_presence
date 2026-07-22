//go:build !linux && !darwin && !freebsd && !windows

package presence

import "net"

// validatePeerCredentials is a no-op on platforms where x/sys/unix does not
// expose a supported API for retrieving Unix socket peer credentials.
func validatePeerCredentials(_ net.Conn, _ int) error {
	return nil
}
