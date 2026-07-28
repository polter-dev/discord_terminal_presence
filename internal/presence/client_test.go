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
	"unicode/utf8"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
	"github.com/polter-dev/discord_terminal_presence/internal/terminaltext"
)

type clearDeadlineAfterPeerCloseConn struct {
	net.Conn
	peerClosed <-chan struct{}
}

func (c clearDeadlineAfterPeerCloseConn) SetReadDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		<-c.peerClosed
	}
	return c.Conn.SetReadDeadline(deadline)
}

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

// TestSanitizeActivityStripsControlCharactersFromEveryField proves the #419
// fix at the sanitizeActivity unit level: every Discord-facing text field —
// name, details, state, both image keys, both image tooltips, and button
// labels — loses its control characters, and an adjacent multi-byte
// character survives. #397 was caused by covering only 2 of 4 fields that
// needed a rule; #422 review caught that the first version of this fix
// covered 5 of 7 (missing both image keys), so this test enumerates all
// seven explicitly.
func TestSanitizeActivityStripsControlCharactersFromEveryField(t *testing.T) {
	dirty := Activity{
		Name:    "\x1b[31mEvil\x07Tool",
		Details: "det\x00ails",
		State:   "sta\rte",
		LargeImage: Image{
			Key:  "large\x1bkey",
			Text: "large\x1btext",
		},
		SmallImage: Image{
			Key:  "small\x07key",
			Text: "small\x07text",
		},
		Buttons: []Button{
			{Label: "Go\x07odé", URL: "https://example.test"},
		},
	}

	clean := sanitizeActivity(dirty)

	const controlBytes = "\x1b\x07\x00\r"
	fields := map[string]string{
		"Name":            clean.Name,
		"Details":         clean.Details,
		"State":           clean.State,
		"LargeImage.Key":  clean.LargeImage.Key,
		"LargeImage.Text": clean.LargeImage.Text,
		"SmallImage.Key":  clean.SmallImage.Key,
		"SmallImage.Text": clean.SmallImage.Text,
	}
	for name, value := range fields {
		if strings.ContainsAny(value, controlBytes) {
			t.Errorf("%s = %q, still contains a control character", name, value)
		}
	}
	if len(clean.Buttons) != 1 || strings.ContainsAny(clean.Buttons[0].Label, controlBytes) {
		t.Fatalf("button label = %+v, still contains a control character", clean.Buttons)
	}
	if !strings.Contains(clean.Buttons[0].Label, "é") {
		t.Fatalf("button label = %q, lost adjacent multi-byte character", clean.Buttons[0].Label)
	}
	if clean.Buttons[0].URL != dirty.Buttons[0].URL {
		t.Fatalf("button URL = %q, must not be touched by text sanitization", clean.Buttons[0].URL)
	}
}

// TestSetActivityWireHasNoRawControlOrBidiRunes is the structural backstop
// Blocking-2 of the #422 review asked for: instead of enumerating fields (an
// earlier version of this fix enumerated 5 of the 7 that needed it and
// missed large_image/small_image), it marshals the real wire payload to JSON,
// decodes it into a generic tree, and recursively visits every string leaf.
// A field added to activityPayload/assetsPayload/buttonPayload in the future
// without going through sanitizeActivity first will fail this test
// automatically, with no enumeration to keep in sync.
func TestSetActivityWireHasNoRawControlOrBidiRunes(t *testing.T) {
	activity := sanitizeActivity(Activity{
		Name:    "\x1b[31mEvil\x07Tool",
		Details: "det\x00ails",
		State:   "sta\rte",
		LargeImage: Image{
			Key:  "large\x1bkey",
			Text: "large\x1btext",
		},
		SmallImage: Image{
			Key:  "small\x07key",
			Text: "small\x07text",
		},
		Buttons: []Button{
			{Label: "Go\x07od", URL: "https://example.test"},
		},
	})

	encoded, err := json.Marshal(newSetActivityPayload(activity, 42, "test-nonce"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	assertNoRawControlOrBidiRunes(t, decoded, "$")
}

// assertNoRawControlOrBidiRunes recursively visits every string leaf of a
// generically decoded JSON value (map[string]any / []any / string) and fails
// the test if any leaf contains a control character or bidi formatting
// control, per terminaltext.IsControlOrBidi.
func assertNoRawControlOrBidiRunes(t *testing.T, value any, path string) {
	t.Helper()
	switch v := value.(type) {
	case string:
		for _, r := range v {
			if terminaltext.IsControlOrBidi(r) {
				t.Errorf("%s = %q contains disallowed rune %U", path, v, r)
			}
		}
	case map[string]any:
		for key, child := range v {
			assertNoRawControlOrBidiRunes(t, child, path+"."+key)
		}
	case []any:
		for i, child := range v {
			assertNoRawControlOrBidiRunes(t, child, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

// TestSetActivitySanitizesControlCharactersEndToEnd proves the sanitization
// happens on the real SetActivity call path (not just on sanitizeActivity in
// isolation) by capturing the exact bytes written to the wire over a real
// frame round trip, including both image keys — the fields #422 review found
// unsanitized in the first version of this fix.
func TestSetActivitySanitizesControlCharactersEndToEnd(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	peerClosed := make(chan struct{})
	client := &RichClient{conn: clearDeadlineAfterPeerCloseConn{Conn: clientConn, peerClosed: peerClosed}}
	defer client.Logout()

	captured := make(chan []byte, 1)
	serverErr := make(chan error, 1)
	go func() {
		defer func() {
			serverConn.Close()
			close(peerClosed)
		}()
		frame, err := readFrame(serverConn)
		if err != nil {
			serverErr <- err
			return
		}
		captured <- append([]byte(nil), frame.payload...)
		if err := writeJSONFrame(serverConn, opcodeFrame, map[string]any{"evt": "READY", "data": map[string]any{}}); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	err := client.SetActivity(Activity{
		Name:    "Evil\x07Tool",
		Details: "Using \x1b[31mtool\x1b[0m",
		State:   "ok",
		LargeImage: Image{
			Key:  "key\x1bimg",
			Text: "Evil\x1bTool",
		},
		SmallImage: Image{
			Key:  "small\x07key",
			Text: "small\x07text",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	frame := <-captured
	// JSON always escapes control bytes as \uXXXX, so checking the raw wire
	// bytes for 0x1b/0x07 would pass even with the defect present. Decode
	// the frame the way the Discord-side JSON parser would, and check the
	// resulting logical string values instead.
	var payload setActivityPayload
	if err := json.Unmarshal(frame, &payload); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(payload.Args.Activity.Name, "\x1b\x07") {
		t.Fatalf("decoded activity name = %q, still contains a control character", payload.Args.Activity.Name)
	}
	if strings.ContainsAny(payload.Args.Activity.Details, "\x1b\x07") {
		t.Fatalf("decoded activity details = %q, still contains a control character", payload.Args.Activity.Details)
	}
	if payload.Args.Activity.Assets == nil {
		t.Fatal("decoded payload has no assets")
	}
	if strings.ContainsAny(payload.Args.Activity.Assets.LargeImage, "\x1b\x07") {
		t.Fatalf("decoded large_image = %q, still contains a control character", payload.Args.Activity.Assets.LargeImage)
	}
	if strings.ContainsAny(payload.Args.Activity.Assets.LargeText, "\x1b\x07") {
		t.Fatalf("decoded large_text = %q, still contains a control character", payload.Args.Activity.Assets.LargeText)
	}
	if strings.ContainsAny(payload.Args.Activity.Assets.SmallImage, "\x1b\x07") {
		t.Fatalf("decoded small_image = %q, still contains a control character", payload.Args.Activity.Assets.SmallImage)
	}
	if strings.ContainsAny(payload.Args.Activity.Assets.SmallText, "\x1b\x07") {
		t.Fatalf("decoded small_text = %q, still contains a control character", payload.Args.Activity.Assets.SmallText)
	}
	if err := <-serverErr; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
}

// TestSetActivityValidatesSanitizedLengthNotRawLength proves the #422 review's
// second ordering finding: sanitizing after validating checks bytes that are
// not the bytes actually sent. SetActivity must sanitize before validating,
// so a value that only crosses Discord's length bound *after* substitution
// (an embedded newline expands to the 3-rune " ; " separator) is caught, and
// a value that only drops below the minimum after control characters are
// stripped is also caught — instead of both silently reaching
// newSetActivityPayload with the wrong bytes already validated.
func TestSetActivityValidatesSanitizedLengthNotRawLength(t *testing.T) {
	t.Run("expands past the maximum on substitution", func(t *testing.T) {
		// 127 runes raw (passes the raw <=128 check); the embedded newline
		// becomes " ; " (3 runes) once sanitized, mid-string so it is not
		// trimmed as a leading/trailing separator, yielding 129 sanitized
		// runes, which must be rejected.
		details := strings.Repeat("a", 63) + "\n" + strings.Repeat("a", 63)
		if got := utf8.RuneCountInString(details); got != 127 {
			t.Fatalf("test setup: raw details = %d runes, want 127", got)
		}
		client := &RichClient{}
		err := client.SetActivity(Activity{Name: "x", Details: details, State: "ok"})
		if err == nil || !strings.Contains(err.Error(), "details must be at most 128 characters") {
			t.Fatalf("SetActivity() error = %v, want a max-length error reflecting the sanitized (129-rune) value", err)
		}
	})

	t.Run("drops below the minimum on stripping", func(t *testing.T) {
		// "a\x1b" is 2 raw runes (passes the raw >=2 check); ESC is stripped
		// entirely, leaving 1 sanitized rune, which must be rejected.
		client := &RichClient{}
		err := client.SetActivity(Activity{Name: "x", Details: "a\x1b", State: "ok"})
		if err == nil || !strings.Contains(err.Error(), "details must be at least 2 characters") {
			t.Fatalf("SetActivity() error = %v, want a minimum-length error reflecting the sanitized (1-rune) value", err)
		}
	})
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
	peerClosed := make(chan struct{})
	client := &RichClient{conn: clearDeadlineAfterPeerCloseConn{Conn: clientConn, peerClosed: peerClosed}}
	defer client.Logout()

	serverErr := make(chan error, 1)
	go func() {
		defer func() {
			serverConn.Close()
			close(peerClosed)
		}()
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
	if client.conn == nil {
		t.Fatal("client closed healthy connection after Discord payload rejection")
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
