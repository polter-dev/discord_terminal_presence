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

// TestNormalizeActivityStripsControlCharactersFromEveryField proves the #419
// fix at the normalizeActivity unit level: every Discord-facing text field —
// name, details, state, both image keys, both image tooltips, and button
// labels — loses its control characters, and an adjacent multi-byte
// character survives. #397 was caused by covering only 2 of 4 fields that
// needed a rule; #422 review caught that the first version of this fix
// covered 5 of 7 (missing both image keys), so this test enumerates all
// seven explicitly.
func TestNormalizeActivityStripsControlCharactersFromEveryField(t *testing.T) {
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

	clean := normalizeActivity(dirty)

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
// without going through normalizeActivity first will fail this test
// automatically, with no enumeration to keep in sync.
func TestSetActivityWireHasNoRawControlOrBidiRunes(t *testing.T) {
	activity := normalizeActivity(Activity{
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

	// #444: normalizeActivity deliberately does not sanitize URLs (doing so
	// would corrupt them), so the structural walk above cannot see a
	// control/bidi rune arriving via an image or button URL — the payload
	// above only used a clean URL. The actual defense for URLs is
	// validateActivity (registry.ValidateButtons / registry.ValidateHTTPURL)
	// rejecting the activity outright before SetActivity ever builds a wire
	// payload for it. Prove that rejection holds for both call sites: if
	// either goes back to accepting these runes, an activity carrying one
	// would sail through validateActivity and reach the wire unchecked,
	// exactly like the button above.
	bidiButtonActivity := normalizeActivity(Activity{
		Name:    "Clean",
		Buttons: []Button{{Label: "Go", URL: "https://example.test/a\u202ebcd"}},
	})
	if err := validateActivity(bidiButtonActivity); err == nil {
		t.Fatal("validateActivity() = nil for a bidi-rune button URL, want rejection")
	}

	bidiImageActivity := normalizeActivity(Activity{
		Name:       "Clean",
		LargeImage: Image{URL: "https://example.test/a\u202ebcd"},
	})
	if err := validateActivity(bidiImageActivity); err == nil {
		t.Fatal("validateActivity() = nil for a bidi-rune image URL, want rejection")
	}
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
// happens on the real SetActivity call path (not just on normalizeActivity in
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

// captureSetActivity runs one real SetActivity call over a net.Pipe, answers
// the frame the way Discord would, and returns the SET_ACTIVITY payload exactly
// as it was decoded from the wire. It exercises the whole normalize → validate →
// encode path rather than any single helper.
func captureSetActivity(t *testing.T, activity Activity) setActivityPayload {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	peerClosed := make(chan struct{})
	client := &RichClient{conn: clearDeadlineAfterPeerCloseConn{Conn: clientConn, peerClosed: peerClosed}}
	t.Cleanup(func() { client.Logout() })

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

	if err := client.SetActivity(activity); err != nil {
		t.Fatalf("SetActivity() error = %v, want the activity to be trimmed to fit and published", err)
	}
	frame := <-captured
	var payload setActivityPayload
	if err := json.Unmarshal(frame, &payload); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	return payload
}

// TestSetActivityBoundsSanitizedLengthNotRawLength is the #436 regression test.
// #422 correctly moved sanitization before validation so length checks run on
// the bytes actually sent, but bounding still ran before sanitization, and
// SanitizeSingleLine expands each line break into a 3-rune " ; " separator. A
// value bounded to exactly 128 runes could therefore reach validateActivity at
// 212 runes and be rejected, so *nothing* was published for that field — a
// directory or custom tool name with a few line breaks was enough. The bound
// now runs after sanitization, so the field is trimmed and published instead.
func TestSetActivityBoundsSanitizedLengthNotRawLength(t *testing.T) {
	// Exactly 128 runes (the bound) with several line breaks: sanitization
	// turns each "\n" into " ; " and pushes the value well past 128.
	details := strings.Repeat("a", 32) + "\n" + strings.Repeat("b", 32) + "\n" + strings.Repeat("c", 30) + "\n" + strings.Repeat("d", 31)
	if got := utf8.RuneCountInString(details); got != 128 {
		t.Fatalf("test setup: raw details = %d runes, want 128", got)
	}
	if got := utf8.RuneCountInString(terminaltext.SanitizeSingleLine(details)); got <= maxActivityTextLength {
		t.Fatalf("test setup: sanitized details = %d runes, want more than %d so the defect is reachable", got, maxActivityTextLength)
	}

	payload := captureSetActivity(t, Activity{Name: "x", Details: details, State: "ok"})

	published := payload.Args.Activity.Details
	if published == "" {
		t.Fatal("details was dropped from the published payload; it must be trimmed to fit, not omitted")
	}
	if got := utf8.RuneCountInString(published); got > maxActivityTextLength {
		t.Fatalf("published details = %d runes, want at most %d", got, maxActivityTextLength)
	}
	if !strings.Contains(published, " ; ") {
		t.Fatalf("published details = %q, want the sanitized separator to survive trimming", published)
	}
	if !strings.HasPrefix(published, strings.Repeat("a", 32)) {
		t.Fatalf("published details = %q, want the leading text preserved", published)
	}
	if !strings.HasSuffix(published, "…") {
		t.Fatalf("published details = %q, want an ellipsis marking the trim", published)
	}
	if payload.Args.Activity.State != "ok" {
		t.Fatalf("published state = %q, want the rest of the activity unaffected", payload.Args.Activity.State)
	}
}

// TestSetActivityOmitsFieldThatSanitizesBelowMinimum covers the other direction
// of the same defect: sanitization can also *shorten* a value below Discord's
// 2-rune minimum, which likewise made validateActivity reject the whole update.
// The field is now dropped and the rest of the activity still publishes.
func TestSetActivityOmitsFieldThatSanitizesBelowMinimum(t *testing.T) {
	// "a\x1b" is 2 raw runes; ESC is stripped entirely, leaving 1 rune.
	payload := captureSetActivity(t, Activity{Name: "x", Details: "a\x1b", State: "ok"})
	if payload.Args.Activity.Details != "" {
		t.Fatalf("published details = %q, want the sub-minimum field omitted", payload.Args.Activity.Details)
	}
	if payload.Args.Activity.Name != "x" || payload.Args.Activity.State != "ok" {
		t.Fatalf("published activity = %+v, want the remaining fields published", payload.Args.Activity)
	}
}

// TestSetActivityFromDetectionWithLineBreaksPublishes drives the same defect
// from the user-facing entry point rather than a hand-built Activity: a tool
// display name containing line breaks flows through ActivityFromDetection
// (which bounds pre-sanitization) into SetActivity. Before the fix the whole
// presence update was rejected; the tool name must now appear, trimmed.
//
// Note on the input: this test constructs registry.Tool directly, which is not
// a path a *config-sourced* custom tool can take — ValidateCustomTool rejects a
// display_name containing U+000A at config load. The production carrier for
// this defect is a directory name, which is read at runtime from the detected
// process's cwd and so takes no config-load path at all. The display name is
// used here only because it reaches both name and details in one activity.
func TestSetActivityFromDetectionWithLineBreaksPublishes(t *testing.T) {
	displayName := strings.Repeat("n", 40) + "\n" + strings.Repeat("m", 40) + "\n" + strings.Repeat("o", 46)
	if got := utf8.RuneCountInString(displayName); got != 128 {
		t.Fatalf("test setup: raw display name = %d runes, want 128", got)
	}
	tool := registry.Tool{ID: "custom", DisplayName: displayName}
	activity, ok := ActivityFromDetection(detector.Detection{Tool: tool, Featured: detector.FeaturedTool{Tool: tool}}, DefaultDisplayOptions())
	if !ok {
		t.Fatal("expected active detection to produce activity")
	}

	payload := captureSetActivity(t, activity)
	if payload.Args.Activity.Details == "" {
		t.Fatal("details was dropped; a tool name with line breaks must still publish")
	}
	for name, value := range map[string]string{
		"name":    payload.Args.Activity.Name,
		"details": payload.Args.Activity.Details,
	} {
		if got := utf8.RuneCountInString(value); got > maxActivityTextLength {
			t.Fatalf("published %s = %d runes, want at most %d", name, got, maxActivityTextLength)
		}
	}
}

// TestNormalizeActivityDoesNotMutateCallerButtons guards the one piece of an
// Activity that is not copied by value: the Buttons slice shares its backing
// array with the caller, and normalizeActivity's field setters write through
// a.Buttons[i].Label. writer.go holds a `desired` Activity across ticks and
// derives `rejected` from it, so normalizing must not reach back into caller
// state. sanitizeActivity allocated a fresh slice for this reason; the guard
// has to survive its removal.
func TestNormalizeActivityDoesNotMutateCallerButtons(t *testing.T) {
	const rawLabel = "ctrl\x1bX label"
	callerButtons := []Button{{Label: rawLabel, URL: "https://example.test"}}

	assertCallerIntact := func(t *testing.T, after string) {
		t.Helper()
		if callerButtons[0].Label != rawLabel {
			t.Fatalf("caller's button label = %q after %s, want it untouched (%q)", callerButtons[0].Label, after, rawLabel)
		}
	}

	normalized := normalizeActivity(Activity{Name: "x", Buttons: callerButtons})
	assertCallerIntact(t, "normalizeActivity")
	if len(normalized.Buttons) != 1 || strings.ContainsRune(normalized.Buttons[0].Label, '\x1b') {
		t.Fatalf("normalized buttons = %+v, want the returned copy sanitized", normalized.Buttons)
	}

	payload := captureSetActivity(t, Activity{Name: "x", Buttons: callerButtons})
	assertCallerIntact(t, "SetActivity")
	if len(payload.Args.Activity.Buttons) != 1 || strings.ContainsRune(payload.Args.Activity.Buttons[0].Label, '\x1b') {
		t.Fatalf("published buttons = %+v, want a sanitized label on the wire", payload.Args.Activity.Buttons)
	}
}

// wireStringBounds maps every string-valued key of the marshaled SET_ACTIVITY
// payload to the maximum rune count it is allowed to carry. It is deliberately
// exhaustive: TestSetActivityWireStringsAreBounded fails on any string key it
// does not find here, so a new Discord-facing field cannot be added to the wire
// payload without someone confronting its bound — and the only way to satisfy
// the bound is to route the field through normalizeActivity's field list.
var wireStringBounds = map[string]int{
	"cmd":         len("SET_ACTIVITY"),
	"nonce":       36,
	"name":        maxActivityTextLength,
	"details":     maxActivityTextLength,
	"state":       maxActivityTextLength,
	"large_text":  maxActivityTextLength,
	"small_text":  maxActivityTextLength,
	"large_image": maxImageValueLength,
	"small_image": maxImageValueLength,
	"label":       registry.MaxButtonLabelLength,
	"url":         registry.MaxButtonURLLength,
}

// TestSetActivityWireStringsAreBounded is the structural counterpart to
// TestSetActivityWireHasNoRawControlOrBidiRunes for #436. Rather than listing
// the fields that need bounding — the enumeration that let #402 miss both image
// tooltips and #422 miss both image keys — it feeds every text field a value
// that only exceeds its bound *after* sanitization, then walks every string
// leaf of the real marshaled payload and checks it against its registered
// bound. A field that bypasses normalizeActivity fails here automatically.
func TestSetActivityWireStringsAreBounded(t *testing.T) {
	grow := func(runes int, filler string) string {
		// Half the runes as line breaks, so SanitizeSingleLine triples them.
		value := strings.Repeat(filler+"\n", runes/2)
		return value + strings.Repeat(filler, runes-utf8.RuneCountInString(value))
	}
	payload := captureSetActivity(t, Activity{
		Name:    grow(maxActivityTextLength, "a"),
		Details: grow(maxActivityTextLength, "b"),
		State:   grow(maxActivityTextLength, "c"),
		LargeImage: Image{
			Key:  grow(maxImageValueLength, "d"),
			Text: grow(maxActivityTextLength, "e"),
		},
		SmallImage: Image{
			Key:  grow(maxImageValueLength, "f"),
			Text: grow(maxActivityTextLength, "g"),
		},
		Buttons: []Button{
			{Label: grow(registry.MaxButtonLabelLength, "h"), URL: "https://example.test"},
		},
	})

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	visited := assertWireStringsBounded(t, decoded, "$", "")
	if visited == 0 {
		t.Fatal("walked no string leaves; the payload capture is not exercising the wire")
	}
}

// assertWireStringsBounded recursively visits every string leaf of a generically
// decoded JSON value, checking it against wireStringBounds under the key it was
// found at. An unregistered key is a failure, not a skip.
func assertWireStringsBounded(t *testing.T, value any, path, key string) int {
	t.Helper()
	switch v := value.(type) {
	case string:
		bound, ok := wireStringBounds[key]
		if !ok {
			t.Errorf("%s: wire field %q has no registered bound; route it through normalizeActivity's activityTextFields list and add it to wireStringBounds", path, key)
			return 1
		}
		if got := utf8.RuneCountInString(v); got > bound {
			t.Errorf("%s = %d runes, want at most %d (value %q)", path, got, bound, v)
		}
		return 1
	case map[string]any:
		count := 0
		for childKey, child := range v {
			count += assertWireStringsBounded(t, child, path+"."+childKey, childKey)
		}
		return count
	case []any:
		count := 0
		for i, child := range v {
			count += assertWireStringsBounded(t, child, fmt.Sprintf("%s[%d]", path, i), key)
		}
		return count
	}
	return 0
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
