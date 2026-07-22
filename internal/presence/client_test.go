package presence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

func TestSetActivityPayloadIncludesFeaturedToolName(t *testing.T) {
	detection := detector.Detection{
		Featured: detector.FeaturedTool{
			Tool: registry.Tool{
				ID:          "claude-code",
				DisplayName: "Claude Code",
			},
		},
		Tool: registry.Tool{
			ID:          "codex-cli",
			DisplayName: "Codex CLI",
		},
	}
	activity, ok := ActivityFromDetection(detection, DefaultDisplayOptions())
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}

	encoded, err := json.Marshal(newSetActivityPayload(activity, 42, "test-nonce"))
	if err != nil {
		t.Fatal(err)
	}
	var payload setActivityPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Args.Activity.Name != "Claude Code" {
		t.Fatalf("activity name = %q, want featured tool display name", payload.Args.Activity.Name)
	}
	if !bytes.Contains(encoded, []byte(`"name":"Claude Code"`)) {
		t.Fatalf("payload does not include activity name: %s", encoded)
	}
}

func TestSetActivityPayloadOmitsEmptyLargeImage(t *testing.T) {
	activity := Activity{
		Name:       "Claude Code",
		LargeImage: Image{Text: "Claude Code"},
		SmallImage: Image{Key: "codex-cli", Text: "Codex CLI"},
	}
	encoded, err := json.Marshal(newSetActivityPayload(activity, 42, "test-nonce"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"large_image"`)) {
		t.Fatalf("payload includes empty large_image: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"small_image":"codex-cli"`)) {
		t.Fatalf("payload does not include non-empty small_image: %s", encoded)
	}

	activity.SmallImage = Image{}
	encoded, err = json.Marshal(newSetActivityPayload(activity, 42, "test-nonce"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"assets"`)) {
		t.Fatalf("payload includes assets with no images: %s", encoded)
	}
}

func TestSetActivitySurfacesDiscordErrorResponse(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := &RichClient{conn: clientConn}
	defer client.Logout()

	serverErr := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		frame, err := readFrame(serverConn)
		if err != nil {
			serverErr <- err
			return
		}
		if frame.opcode != opcodeFrame {
			serverErr <- fmt.Errorf("opcode = %d, want %d", frame.opcode, opcodeFrame)
			return
		}
		if err := writeJSONFrame(serverConn, opcodeFrame, map[string]any{
			"evt": "ERROR",
			"data": map[string]any{
				"code":    4000,
				"message": "large_image is not allowed to be empty",
			},
		}); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	err := client.SetActivity(Activity{Name: "Claude Code"})
	if err == nil {
		t.Fatal("SetActivity error = nil, want Discord error")
	}
	if !strings.Contains(err.Error(), "4000") || !strings.Contains(err.Error(), "large_image is not allowed to be empty") {
		t.Fatalf("SetActivity error = %q, want code and message", err)
	}
	if client.conn != nil {
		t.Fatal("client retained connection after SET_ACTIVITY error")
	}
	if serverErr := <-serverErr; serverErr != nil && !errors.Is(serverErr, net.ErrClosed) {
		t.Fatal(serverErr)
	}
}

func TestRichClientLogoutWithoutConnection(t *testing.T) {
	client := &RichClient{}
	if err := client.Logout(); err != nil {
		t.Fatal(err)
	}
}

func TestSetActivityTimesOutWhenPeerStalls(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := &RichClient{conn: clientConn, ioTimeout: 25 * time.Millisecond}
	defer serverConn.Close()

	requestRead := make(chan error, 1)
	go func() {
		_, err := readFrame(serverConn)
		requestRead <- err
		// Deliberately leave the response unread until the client deadline fires.
	}()

	started := time.Now()
	err := client.SetActivity(Activity{Name: "Claude Code"})
	if err == nil {
		t.Fatal("SetActivity error = nil, want timeout")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("SetActivity error = %v, want net timeout error", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("SetActivity took %v, want bounded timeout", elapsed)
	}
	if client.conn != nil {
		t.Fatal("client retained connection after timeout")
	}
	if err := <-requestRead; err != nil {
		t.Fatalf("server read request: %v", err)
	}
}
