//go:build windows

package presence

import "testing"

// TestDiscordIPCPipeExistsRejectsUnencodablePath covers #503: a path that
// cannot be converted to UTF-16 (e.g. an embedded NUL from an untrusted
// DISCORD_IPC_PATH override) can never name a real named pipe, so it must be
// classified as not-existing rather than misreported as present.
func TestDiscordIPCPipeExistsRejectsUnencodablePath(t *testing.T) {
	unencodable := "\\\\.\\pipe\\discord-ipc-0\x00trailing"
	if discordIPCPipeExists(unencodable) {
		t.Fatal("expected an unencodable path to be classified as not existing")
	}
}

// TestDiscordIPCPipeExistsMissingPipe covers the normal not-found path: a
// well-formed but nonexistent pipe name must classify as not existing, and
// must return promptly rather than blocking on WaitNamedPipeW's default wait.
func TestDiscordIPCPipeExistsMissingPipe(t *testing.T) {
	if discordIPCPipeExists(`\\.\pipe\termp-test-nonexistent-503`) {
		t.Fatal("expected a nonexistent pipe to be classified as not existing")
	}
}
