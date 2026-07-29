//go:build !windows

package presence

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// newStaleIPCSocket binds a real Unix socket, proves a client can connect to
// it, then abandons the listener without unlinking the inode. The result is
// byte-for-byte the on-disk state a crashed or killed Discord leaves behind:
// a socket file with no listener, where connect(2) returns ECONNREFUSED.
//
// The directory layout deliberately mirrors newIsolatedIPCSocket: a short
// path (macOS caps sun_path near 104 bytes, and t.TempDir() overflows it) and
// never one level under /tmp, which production globs as /tmp/*/discord-ipc-*.
//
// It returns the socket's own directory, not the outer
// temporary directory newIsolatedIPCSocket hands back. Production discovery
// reaches the socket either way — directly when a base dir is the socket's own
// directory, or via the one-level /tmp/*/discord-ipc-* glob from the outer one
// — but the inner directory exercises direct candidate enumeration rather than
// the glob, and it matches what newNonSocketCandidate and newAbsentCandidate
// return, so all three rows of the classification table are scanned the same
// way and the absent-path control stays a like-for-like comparison.
func newStaleIPCSocket(t *testing.T) (socketDir, socketPath string) {
	t.Helper()
	_, socketPath, listener := newIsolatedIPCSocket(t)
	socketDir = filepath.Dir(socketPath)

	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener type = %T, want *net.UnixListener", listener)
	}

	// Prove the listener really bound and accepts before we abandon it. A
	// silently failed bind would make every assertion below vacuous.
	accepted := make(chan error, 1)
	go func() {
		conn, err := unixListener.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		accepted <- err
	}()
	probe, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("live socket did not accept a connection: %v", err)
	}
	_ = probe.Close()
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("live socket accept failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live socket never accepted; harness is broken")
	}

	unixListener.SetUnlinkOnClose(false)
	if err := unixListener.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("stale socket disappeared: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("stale socket mode = %v, want a Unix socket", info.Mode())
	}
	return socketDir, socketPath
}

// newLiveIPCSocket binds a real Unix socket and leaves a listener accepting on
// it, so a scan that reaches the socket connects successfully. It returns the
// socket's own directory, matching newStaleIPCSocket/newNonSocketCandidate/
// newAbsentCandidate so every row of the classification table is scanned the
// same way.
//
// It proves the listener bound *and* accepted a connection before returning. A
// bind that failed silently would make the positive control vacuous — which is
// the exact failure mode the control exists to remove.
func newLiveIPCSocket(t *testing.T) (socketDir, socketPath string) {
	t.Helper()
	_, socketPath, listener := newIsolatedIPCSocket(t)
	socketDir = filepath.Dir(socketPath)

	firstAccept := make(chan error, 1)
	go func() {
		// Discord's real peer keeps the connection open, so this one holds
		// every accepted conn until the listener closes. Closing eagerly
		// would race the dialer's own post-connect validation.
		var accepted []net.Conn
		defer func() {
			for _, conn := range accepted {
				_ = conn.Close()
			}
		}()
		first := true
		for {
			conn, err := listener.Accept()
			if first {
				firstAccept <- err
				first = false
			}
			if err != nil {
				return
			}
			accepted = append(accepted, conn)
		}
	}()

	probe, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("live socket did not accept a connection: %v", err)
	}
	_ = probe.Close()
	select {
	case err := <-firstAccept:
		if err != nil {
			t.Fatalf("live socket accept failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live socket never accepted; harness is broken")
	}
	return socketDir, socketPath
}

// newNonSocketCandidate creates a plain regular file named discord-ipc-0.
func newNonSocketCandidate(t *testing.T) (dir, path string) {
	t.Helper()
	outer, err := os.MkdirTemp("/tmp", "termp-nsk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outer) })
	dir = filepath.Join(outer, "s")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "discord-ipc-0")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

// newAbsentCandidate is the control: an existing, well-formed directory with
// no discord-ipc-* entry in it at all.
func newAbsentCandidate(t *testing.T) (dir, path string) {
	t.Helper()
	outer, err := os.MkdirTemp("/tmp", "termp-abs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outer) })
	dir = filepath.Join(outer, "s")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Join(dir, "discord-ipc-0")
}

// TestDialDiscordIPCSocketEndpointEvidence pins the per-candidate exists flag
// (issue #468). The flag is what dialDiscordIPC turns into endpointFound and
// therefore what selects ErrDiscordIPCUnreachable over ErrDiscordIPCNotFound.
func TestDialDiscordIPCSocketEndpointEvidence(t *testing.T) {
	t.Run("stale socket is not an endpoint", func(t *testing.T) {
		_, socketPath := newStaleIPCSocket(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, exists, err := dialDiscordIPCSocket(ctx, socketPath)
		if conn != nil {
			_ = conn.Close()
			t.Fatal("dialed a stale socket successfully; harness is broken")
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("stale socket dial error = %v, want ECONNREFUSED", err)
		}
		t.Logf("STALE SOCKET: exists=%v err=%v", exists, err)
		if exists {
			t.Error("stale socket reported exists=true; ECONNREFUSED proves nothing is listening")
		}
	})

	t.Run("non-socket file is not an endpoint", func(t *testing.T) {
		_, path := newNonSocketCandidate(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, exists, err := dialDiscordIPCSocket(ctx, path)
		if conn != nil {
			_ = conn.Close()
			t.Fatal("dialed a regular file successfully; harness is broken")
		}
		t.Logf("NON-SOCKET FILE: exists=%v err=%v", exists, err)
		if exists {
			t.Error("non-socket file reported exists=true; nothing there could be Discord")
		}
	})

	t.Run("absent path is not an endpoint (control)", func(t *testing.T) {
		_, path := newAbsentCandidate(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, exists, err := dialDiscordIPCSocket(ctx, path)
		if conn != nil {
			_ = conn.Close()
			t.Fatal("dialed an absent path successfully; harness is broken")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("absent path error = %v, want os.ErrNotExist", err)
		}
		t.Logf("ABSENT (control): exists=%v err=%v", exists, err)
		if exists {
			t.Error("absent path reported exists=true")
		}
	})

	t.Run("live socket is an endpoint (control)", func(t *testing.T) {
		_, socketPath, listener := newIsolatedIPCSocket(t)
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, exists, err := dialDiscordIPCSocket(ctx, socketPath)
		if conn != nil {
			_ = conn.Close()
		}
		t.Logf("LIVE SOCKET (control): exists=%v err=%v", exists, err)
		if !exists {
			t.Error("live socket reported exists=false")
		}
	})
}

// scanClassification points the whole candidate search at dir and returns the
// sentinel dialDiscordIPC settles on. fixturePath, when non-empty, is the
// candidate the caller planted; it is asserted to be a direct child of dir and
// to still be there. Without that assertion a wrong dir would make the scan
// find nothing and the test would pass vacuously — reporting "not found"
// because there was nothing to classify rather than because the fix works.
func scanClassification(t *testing.T, dir, fixturePath string) error {
	t.Helper()
	assertFixturePresent(t, dir, fixturePath)
	conn, err := runScan(t, map[string]string{"XDG_RUNTIME_DIR": dir})
	if conn != nil {
		_ = conn.Close()
		t.Fatal("scan connected to something; harness is not isolated")
	}
	return err
}

// assertFixturePresent guards against a wrong dir making the scan find nothing.
func assertFixturePresent(t *testing.T, dir, fixturePath string) {
	t.Helper()
	if fixturePath == "" {
		return
	}
	if got := filepath.Dir(fixturePath); got != dir {
		t.Fatalf("fixture %q is not directly inside the scanned dir %q (got %q)", fixturePath, dir, got)
	}
	if _, err := os.Lstat(fixturePath); err != nil {
		t.Fatalf("fixture disappeared before the scan: %v", err)
	}
}

// runScan drives the full dialDiscordIPC scan with every base-directory
// environment variable set explicitly: those named in dirs get that value,
// every other one is cleared. Clearing rather than inheriting is what makes a
// single-source subtest meaningful — otherwise the ambient TMPDIR could reach
// the fixture and the test would pass no matter which name production reads.
// t.Setenv also restores the previous value, so no subtest leaks into the next.
func runScan(t *testing.T, dirs map[string]string) (net.Conn, error) {
	t.Helper()
	// dialDiscordIPC always appends /tmp to its base dirs, so a real Discord
	// socket there would contribute an endpoint this harness does not
	// control and the assertion would fail for the wrong reason.
	for _, pattern := range []string{"/tmp/discord-ipc-*", "/tmp/*/discord-ipc-*"} {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			t.Skipf("cannot isolate scan: %s matches %v", pattern, matches)
		}
	}
	for _, name := range []string{"XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, dirs[name])
	}
	t.Setenv("DISCORD_IPC_PATH", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return dialDiscordIPC(ctx)
}

// TestDialDiscordIPCScanClassification is the end-to-end half of #468: it
// asserts the sentinel `termp status` actually renders from.
func TestDialDiscordIPCScanClassification(t *testing.T) {
	t.Run("stale socket scans as not found", func(t *testing.T) {
		dir, socketPath := newStaleIPCSocket(t)
		err := scanClassification(t, dir, socketPath)
		t.Logf("STALE SOCKET scan: %v", err)
		if !errors.Is(err, ErrDiscordIPCNotFound) {
			t.Errorf("scan classified %v, want ErrDiscordIPCNotFound", err)
		}
	})

	t.Run("non-socket file scans as not found", func(t *testing.T) {
		dir, path := newNonSocketCandidate(t)
		err := scanClassification(t, dir, path)
		t.Logf("NON-SOCKET FILE scan: %v", err)
		if !errors.Is(err, ErrDiscordIPCNotFound) {
			t.Errorf("scan classified %v, want ErrDiscordIPCNotFound", err)
		}
	})

	t.Run("absent path scans as not found (control)", func(t *testing.T) {
		dir, _ := newAbsentCandidate(t)
		err := scanClassification(t, dir, "")
		t.Logf("ABSENT (control) scan: %v", err)
		if !errors.Is(err, ErrDiscordIPCNotFound) {
			t.Errorf("scan classified %v, want ErrDiscordIPCNotFound", err)
		}
	})

	// Positive control. Every other row asserts a negative outcome, so the
	// whole table would keep passing if the scan stopped reaching the
	// fixtures at all — a scan that never looks anywhere reports not-found
	// for all three. This row fails the moment that happens.
	t.Run("live socket scans as connected (positive control)", func(t *testing.T) {
		dir, socketPath := newLiveIPCSocket(t)
		assertFixturePresent(t, dir, socketPath)
		conn, err := runScan(t, map[string]string{"XDG_RUNTIME_DIR": dir})
		t.Logf("LIVE SOCKET (positive control) scan: err=%v", err)
		if err != nil {
			t.Fatalf("scan failed to connect to a live socket: %v", err)
		}
		if conn == nil {
			t.Fatal("scan returned no connection and no error")
		}
		_ = conn.Close()
	})
}

// TestDialDiscordIPCScanEnvSources pins live discovery through each base-
// directory environment variable independently. The negative rows above cannot
// distinguish the sources: dropping any single name from dialDiscordIPC's
// envNames left every one of them passing. Each subtest here sets exactly one
// name and clears the rest, so dropping that name from envNames fails exactly
// this subtest.
func TestDialDiscordIPCScanEnvSources(t *testing.T) {
	for _, envName := range []string{"XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP"} {
		t.Run(envName, func(t *testing.T) {
			dir, socketPath := newLiveIPCSocket(t)
			assertFixturePresent(t, dir, socketPath)
			conn, err := runScan(t, map[string]string{envName: dir})
			t.Logf("LIVE SOCKET via %s: err=%v", envName, err)
			if err != nil {
				t.Fatalf("scan did not discover the live socket through %s: %v", envName, err)
			}
			if conn == nil {
				t.Fatal("scan returned no connection and no error")
			}
			_ = conn.Close()
		})
	}
}

// TestDialDiscordIPCSocketStillReportsUnreachable covers the conditions that
// genuinely establish an endpoint we could not use. None of them may be
// downgraded to "not running" by the #468 fix.
func TestDialDiscordIPCSocketStillReportsUnreachable(t *testing.T) {
	t.Run("foreign-uid socket", func(t *testing.T) {
		_, socketPath, _ := newIsolatedIPCSocket(t)
		_, err := validateSocketCandidate(socketPath, os.Geteuid()+1)
		if err == nil {
			t.Fatal("foreign-uid validation succeeded; harness is broken")
		}
		t.Logf("FOREIGN UID: err=%v", err)
		if !validationErrorProvesEndpoint(err) {
			t.Error("foreign-uid socket classified as not-found; someone's Discord is running")
		}
	})

	t.Run("directory resolution failure", func(t *testing.T) {
		_, socketPath, _ := newIsolatedIPCSocket(t)
		if err := os.Chmod(filepath.Dir(socketPath), 0o777); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(socketPath), 0o700) })
		_, err := validateSocketCandidate(socketPath, os.Geteuid())
		if err == nil {
			t.Fatal("world-writable directory accepted; harness is broken")
		}
		t.Logf("WORLD-WRITABLE DIR: err=%v", err)
		if !validationErrorProvesEndpoint(err) {
			t.Error("indeterminate validation failure downgraded to not-found")
		}
	})

	t.Run("connect-then-failed-validation", func(t *testing.T) {
		_, socketPath, listener := newIsolatedIPCSocket(t)
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
		before, err := validateSocketCandidate(socketPath, os.Geteuid())
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.DialTimeout("unix", socketPath, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		// A completed connect proves a listener exists, so the post-connect
		// branch must keep exists=true no matter why validation failed.
		if err := validateConnectedSocket(conn, socketPath, before, os.Geteuid()+1); err == nil {
			t.Fatal("post-connect validation succeeded for a foreign euid; harness is broken")
		} else {
			t.Logf("CONNECT-THEN-FAILED-VALIDATION: err=%v", err)
		}
	})
}

// TestDialErrorProvesEndpoint is the platform-independent decision table for
// the dial branch.
func TestDialErrorProvesEndpoint(t *testing.T) {
	opErr := func(err error) error {
		return &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", err)}
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"bare ECONNREFUSED", syscall.ECONNREFUSED, false},
		{"wrapped ECONNREFUSED", opErr(syscall.ECONNREFUSED), false},
		{"bare ErrNotExist", os.ErrNotExist, false},
		{"wrapped ENOENT", opErr(syscall.ENOENT), false},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"wrapped ETIMEDOUT", opErr(syscall.ETIMEDOUT), true},
		{"wrapped EACCES", opErr(syscall.EACCES), true},
		{"wrapped EPERM", opErr(syscall.EPERM), true},
		{"unknown error", errors.New("something else"), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dialErrorProvesEndpoint(test.err); got != test.want {
				t.Errorf("dialErrorProvesEndpoint(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

// TestValidationErrorProvesEndpoint is the platform-independent decision table
// for the pre-dial validation branch.
func TestValidationErrorProvesEndpoint(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"absent", os.ErrNotExist, false},
		{"wrapped absent", errors.New("x: " + os.ErrNotExist.Error()), true}, // text alone must not match
		{"not a socket", errIPCCandidateNotSocket, false},
		{"wrapped not a socket", errors.New("inspect: " + errIPCCandidateNotSocket.Error()), true},
		{"foreign uid", errIPCCandidateForeignOwner, true},
		{"indeterminate", errors.New("inspect socket directory: i/o error"), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validationErrorProvesEndpoint(test.err); got != test.want {
				t.Errorf("validationErrorProvesEndpoint(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}
