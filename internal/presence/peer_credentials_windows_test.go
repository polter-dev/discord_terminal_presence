//go:build windows

package presence

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestValidatePeerSIDs(t *testing.T) {
	owner := mustSID(t, "S-1-5-21-1-2-3-1001")
	sameOwner := mustSID(t, "S-1-5-21-1-2-3-1001")
	differentOwner := mustSID(t, "S-1-5-21-1-2-3-1002")

	if err := validatePeerSIDs(owner, sameOwner); err != nil {
		t.Fatalf("same owner rejected: %v", err)
	}
	if err := validatePeerSIDs(owner, differentOwner); err == nil {
		t.Fatal("different owner accepted")
	}
	if err := validatePeerSIDs(nil, sameOwner); err == nil {
		t.Fatal("missing current-process SID accepted")
	}
	if err := validatePeerSIDs(owner, nil); err == nil {
		t.Fatal("missing server SID accepted")
	}
}

func TestValidatePipePeerInspectionFailureFailsClosed(t *testing.T) {
	inspectionErr := errors.New("access denied")
	owner := mustSID(t, "S-1-5-21-1-2-3-1001")
	conn := &fakeWindowsConn{}

	tests := []struct {
		name        string
		currentUser func() (*windows.SID, error)
		serverUser  func(net.Conn) (*windows.SID, error)
	}{
		{
			name: "current process token",
			currentUser: func() (*windows.SID, error) {
				return nil, inspectionErr
			},
			serverUser: func(net.Conn) (*windows.SID, error) {
				return owner, nil
			},
		},
		{
			name: "server process token",
			currentUser: func() (*windows.SID, error) {
				return owner, nil
			},
			serverUser: func(net.Conn) (*windows.SID, error) {
				return nil, inspectionErr
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePipePeerWithLookups(
				conn,
				tt.currentUser,
				tt.serverUser,
				func(net.Conn) (string, error) { return "Discord.exe", nil },
				false,
			)
			if !errors.Is(err, inspectionErr) {
				t.Fatalf("error = %v, want wrapped inspection error", err)
			}
		})
	}
}

func TestValidatePipePeerWithLookupsVerifiesServerImageName(t *testing.T) {
	inspectionErr := errors.New("process exited")
	owner := mustSID(t, "S-1-5-21-1-2-3-1001")
	attacker := mustSID(t, "S-1-5-21-1-2-3-1002")
	conn := &fakeWindowsConn{}

	tests := []struct {
		name            string
		serverSID       *windows.SID
		serverImageName func(net.Conn) (string, error)
		wantErr         bool
	}{
		{
			name:      "sid match discord name",
			serverSID: owner,
			serverImageName: func(net.Conn) (string, error) {
				return "Discord.exe", nil
			},
		},
		{
			name:      "sid match non discord name",
			serverSID: owner,
			serverImageName: func(net.Conn) (string, error) {
				return "evil.exe", nil
			},
			wantErr: true,
		},
		{
			name:      "sid match image lookup error",
			serverSID: owner,
			serverImageName: func(net.Conn) (string, error) {
				return "", inspectionErr
			},
		},
		{
			name:      "sid mismatch rejected before image name",
			serverSID: attacker,
			serverImageName: func(net.Conn) (string, error) {
				return "Discord.exe", nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePipePeerWithLookups(
				conn,
				func() (*windows.SID, error) { return owner, nil },
				func(net.Conn) (*windows.SID, error) { return tt.serverSID, nil },
				tt.serverImageName,
				false,
			)
			if tt.wantErr && err == nil {
				t.Fatal("validatePipePeerWithLookups returned nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validatePipePeerWithLookups returned error: %v", err)
			}
		})
	}
}

func TestValidatePipePeerWithLookupsSkipsServerImageNameWithOverride(t *testing.T) {
	owner := mustSID(t, "S-1-5-21-1-2-3-1001")
	conn := &fakeWindowsConn{}

	err := validatePipePeerWithLookups(
		conn,
		func() (*windows.SID, error) { return owner, nil },
		func(net.Conn) (*windows.SID, error) { return owner, nil },
		func(net.Conn) (string, error) {
			t.Fatal("server image name lookup should be skipped when DISCORD_IPC_PATH is set")
			return "", nil
		},
		true,
	)
	if err != nil {
		t.Fatalf("validatePipePeerWithLookups returned error: %v", err)
	}
}

func TestDiscordIPCPathOverrideSetRequiresAbsolutePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "unset", path: "", want: false},
		{name: "relative", path: `discord-ipc-0`, want: false},
		{name: "absolute named pipe", path: `\\.\pipe\discord-ipc-0`, want: true},
		{name: "absolute drive path", path: `C:\Users\test\discord-ipc-0`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := discordIPCPathOverrideSet(tt.path); got != tt.want {
				t.Fatalf("discordIPCPathOverrideSet(%q) = %t, want %t", tt.path, got, tt.want)
			}
		})
	}
}

func TestDialDiscordIPCRejectsReplacedPipeAndTriesNext(t *testing.T) {
	owner := mustSID(t, "S-1-5-21-1-2-3-1001")
	attacker := mustSID(t, "S-1-5-21-1-2-3-1002")
	replaced := &fakeWindowsConn{serverSID: attacker}
	trusted := &fakeWindowsConn{serverSID: owner}
	dialCount := 0

	dial := func(ctx context.Context, path string, timeout time.Duration) (net.Conn, error) {
		if timeout != 500*time.Millisecond {
			t.Fatalf("timeout for %s = %v, want 500ms", path, timeout)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dialCount++
		switch dialCount {
		case 1:
			return replaced, nil
		case 2:
			return trusted, nil
		default:
			return nil, errors.New("unexpected candidate")
		}
	}
	verify := func(conn net.Conn) error {
		candidate := conn.(*fakeWindowsConn)
		return validatePeerSIDs(owner, candidate.serverSID)
	}

	conn, err := dialDiscordIPCWith(context.Background(), "", dial, verify)
	if err != nil {
		t.Fatalf("dialDiscordIPCWith: %v", err)
	}
	if conn != trusted {
		t.Fatalf("returned connection = %p, want trusted connection %p", conn, trusted)
	}
	if !replaced.closed {
		t.Fatal("replaced, wrong-owner pipe connection was not closed")
	}
	if trusted.closed {
		t.Fatal("trusted pipe connection was closed")
	}
}

func TestDialDiscordIPCClosesConnectionOnInspectionFailure(t *testing.T) {
	candidate := &fakeWindowsConn{}
	dialCount := 0
	dial := func(context.Context, string, time.Duration) (net.Conn, error) {
		dialCount++
		if dialCount == 1 {
			return candidate, nil
		}
		return nil, errors.New("not found")
	}
	inspectionErr := errors.New("cannot query process")

	conn, err := dialDiscordIPCWith(context.Background(), "", dial, func(net.Conn) error { return inspectionErr })
	if conn != nil {
		t.Fatalf("connection = %v, want nil", conn)
	}
	if err == nil || !strings.Contains(err.Error(), inspectionErr.Error()) {
		t.Fatalf("error = %v, want inspection failure", err)
	}
	if !candidate.closed {
		t.Fatal("unverified connection was not closed")
	}
}

func TestDialDiscordIPCStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dialStarted := make(chan struct{})
	dial := func(ctx context.Context, _ string, timeout time.Duration) (net.Conn, error) {
		if timeout != 500*time.Millisecond {
			t.Fatalf("timeout = %v, want 500ms", timeout)
		}
		close(dialStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	go func() {
		<-dialStarted
		cancel()
	}()

	started := time.Now()
	conn, err := dialDiscordIPCWith(ctx, "", dial, func(net.Conn) error { return nil })
	if conn != nil {
		t.Fatalf("connection = %v, want nil", conn)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("dial returned after %v, want prompt cancellation", elapsed)
	}
}

// TestDialDiscordIPCWithOverrideAuthoritative reproduces #409 on Windows:
// when DISCORD_IPC_PATH (passed in as override) fails to connect, the default
// pipe candidates must never be dialed.
func TestDialDiscordIPCWithOverrideAuthoritative(t *testing.T) {
	const override = `\\.\pipe\does-not-exist`
	dialed := make([]string, 0, 1)
	dial := func(_ context.Context, path string, _ time.Duration) (net.Conn, error) {
		dialed = append(dialed, path)
		return nil, errors.New("no such pipe")
	}

	conn, err := dialDiscordIPCWith(context.Background(), override, dial, func(net.Conn) error {
		t.Fatal("verify should not run when dial fails")
		return nil
	})
	if conn != nil {
		t.Fatalf("connection = %v, want nil", conn)
	}
	if !errors.Is(err, ErrDiscordIPCNotFound) {
		t.Fatalf("error = %v, want ErrDiscordIPCNotFound", err)
	}
	if !strings.Contains(err.Error(), override) {
		t.Fatalf("error = %v, want it to name the override %q", err, override)
	}
	if len(dialed) != 1 || dialed[0] != override {
		t.Fatalf("dialed = %v, want only the override to be attempted", dialed)
	}
}

// TestDialDiscordIPCWithRejectsRelativeOverrideAsError proves a non-absolute
// override is a hard error, not a logged "override ignored", and that no
// pipe is dialed at all.
func TestDialDiscordIPCWithRejectsRelativeOverrideAsError(t *testing.T) {
	dial := func(context.Context, string, time.Duration) (net.Conn, error) {
		t.Fatal("dial should not run for a non-absolute override")
		return nil, nil
	}
	_, err := dialDiscordIPCWith(context.Background(), "relative-pipe", dial, func(net.Conn) error { return nil })
	if !errors.Is(err, ErrDiscordIPCOverrideInvalid) {
		t.Fatalf("error = %v, want ErrDiscordIPCOverrideInvalid", err)
	}
}

func TestDiscordIPCPipeCandidatesOverrideFirst(t *testing.T) {
	const override = `\\wsl.localhost\Ubuntu\discord-ipc-0`
	got := discordIPCPipeCandidates(override)
	if len(got) != 11 {
		t.Fatalf("candidate count = %d, want 11", len(got))
	}
	if got[0] != override {
		t.Fatalf("first candidate = %q, want override %q", got[0], override)
	}

	defaultOnly := discordIPCPipeCandidates("")
	if len(defaultOnly) != 10 || defaultOnly[0] != `\\.\pipe\discord-ipc-0` {
		t.Fatalf("unset override candidates = %v", defaultOnly)
	}
}

func mustSID(t *testing.T, value string) *windows.SID {
	t.Helper()
	sid, err := windows.StringToSid(value)
	if err != nil {
		t.Fatalf("StringToSid(%q): %v", value, err)
	}
	return sid
}

type fakeWindowsConn struct {
	closed    bool
	serverSID *windows.SID
}

func (*fakeWindowsConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (*fakeWindowsConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *fakeWindowsConn) Close() error                   { c.closed = true; return nil }
func (*fakeWindowsConn) LocalAddr() net.Addr              { return fakeWindowsAddr("local") }
func (*fakeWindowsConn) RemoteAddr() net.Addr             { return fakeWindowsAddr("remote") }
func (*fakeWindowsConn) SetDeadline(time.Time) error      { return nil }
func (*fakeWindowsConn) SetReadDeadline(time.Time) error  { return nil }
func (*fakeWindowsConn) SetWriteDeadline(time.Time) error { return nil }

type fakeWindowsAddr string

func (fakeWindowsAddr) Network() string  { return "pipe" }
func (a fakeWindowsAddr) String() string { return string(a) }
