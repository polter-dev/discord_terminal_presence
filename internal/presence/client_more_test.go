package presence

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type shortWriter struct {
	bytes.Buffer
	limit int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		p = p[:w.limit]
	}
	return w.Buffer.Write(p)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func TestStatusClientUsesShortTimeoutWithoutChangingDaemonDefault(t *testing.T) {
	if got := (&RichClient{}).timeout(); got != defaultIOTimeout {
		t.Fatalf("daemon client timeout = %v, want %v", got, defaultIOTimeout)
	}
	client := newStatusClient(context.Background())
	if got := client.timeout(); got != statusIOTimeout {
		t.Fatalf("status client timeout = %v, want %v", got, statusIOTimeout)
	}
	if statusIOTimeout >= defaultIOTimeout {
		t.Fatalf("status timeout %v is not shorter than daemon timeout %v", statusIOTimeout, defaultIOTimeout)
	}
}

func TestStatusClientReadReturnsPromptlyWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := newStatusClient(ctx)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	result := make(chan error, 1)
	go func() {
		_, err := client.readFrame(clientConn)
		result <- err
	}()

	started := time.Now()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("readFrame error = %v, want context canceled", err)
		}
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("readFrame returned after %v, want prompt cancellation", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("readFrame did not observe cancellation before status timeout %v", statusIOTimeout)
	}
}

func TestIPCFrameRoundTripHandlesShortWrites(t *testing.T) {
	w := &shortWriter{limit: 3}
	value := map[string]any{"evt": "READY", "count": 2}
	if err := writeJSONFrame(w, opcodeFrame, value); err != nil {
		t.Fatal(err)
	}
	frame, err := readFrame(bytes.NewReader(w.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if frame.opcode != opcodeFrame || !strings.Contains(string(frame.payload), `"evt":"READY"`) {
		t.Fatalf("frame = opcode %d payload %s", frame.opcode, frame.payload)
	}
}

func TestWriteJSONFrameErrors(t *testing.T) {
	tests := []struct {
		name  string
		write io.Writer
		value any
		want  string
	}{
		{name: "marshal", write: io.Discard, value: make(chan int), want: "encode JSON"},
		{name: "zero write", write: zeroWriter{}, value: map[string]int{"x": 1}, want: "short write"},
		{name: "writer error", write: errorWriter{}, value: map[string]int{"x": 1}, want: "write frame"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := writeJSONFrame(tt.write, opcodeFrame, tt.value); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestReadFrameRejectsMalformedInput(t *testing.T) {
	header := func(opcode, length uint32) []byte {
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint32(buf[:4], opcode)
		binary.LittleEndian.PutUint32(buf[4:], length)
		return buf
	}
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "short header", data: []byte{1, 2}, want: "frame header"},
		{name: "oversize", data: header(opcodeFrame, maxPayload+1), want: "refusing frame"},
		{name: "short payload", data: append(header(opcodeFrame, 4), 'x'), want: "frame payload"},
		{name: "empty payload", data: header(opcodeHandshake, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame, err := readFrame(bytes.NewReader(tt.data))
			if tt.want == "" {
				if err != nil || frame.opcode != opcodeHandshake || len(frame.payload) != 0 {
					t.Fatalf("frame = %#v, error = %v", frame, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSetActivityResponseMatrix(t *testing.T) {
	tests := []struct {
		name    string
		opcode  uint32
		payload any
		wantErr string
	}{
		{name: "success", opcode: opcodeFrame, payload: map[string]any{"evt": "ACTIVITY_UPDATE"}},
		{name: "wrong opcode", opcode: opcodeHandshake, payload: map[string]any{}, wantErr: "unexpected SET_ACTIVITY response opcode"},
		{name: "bad json", opcode: opcodeFrame, payload: rawJSON("{"), wantErr: "decode SET_ACTIVITY response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			client := &RichClient{conn: clientConn, ioTimeout: time.Second}
			serverErr := make(chan error, 1)
			closeServer := make(chan struct{})
			go func() {
				if _, err := readFrame(serverConn); err != nil {
					serverErr <- err
					_ = serverConn.Close()
					return
				}
				var err error
				if raw, ok := tt.payload.(rawJSON); ok {
					err = writeRawFrame(serverConn, tt.opcode, []byte(raw))
				} else {
					err = writeJSONFrame(serverConn, tt.opcode, tt.payload)
				}
				serverErr <- err
				<-closeServer
				_ = serverConn.Close()
			}()

			err := client.SetActivity(Activity{Name: "Codex"})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if client.conn == nil {
					t.Fatal("successful activity closed connection")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				if client.conn != nil {
					t.Fatal("failed activity retained connection")
				}
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
			close(closeServer)
			if err := client.Logout(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type rawJSON string

func writeRawFrame(w io.Writer, opcode uint32, payload []byte) error {
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[:4], opcode)
	binary.LittleEndian.PutUint32(header[4:], uint32(len(payload)))
	if _, err := w.Write(append(header, payload...)); err != nil {
		return err
	}
	return nil
}

func TestSetActivityRequiresConnection(t *testing.T) {
	if err := (&RichClient{}).SetActivity(Activity{}); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("error = %v, want not connected", err)
	}
}

func TestLoginRequiresApplicationID(t *testing.T) {
	if err := (&RichClient{}).Login(""); err == nil || !strings.Contains(err.Error(), "application ID is required") {
		t.Fatalf("Login error = %v", err)
	}
}

func TestResponseAndPayloadHelpers(t *testing.T) {
	if _, err := parseResponse([]byte("{")); err == nil {
		t.Fatal("parseResponse accepted malformed JSON")
	}
	if err := (ipcResponse{Event: "READY"}).discordError(); err != nil {
		t.Fatal(err)
	}
	response := ipcResponse{Event: "ERROR"}
	response.Data.Code = 4001
	response.Data.Message = "bad request"
	if err := response.discordError(); err == nil || !strings.Contains(err.Error(), "4001") {
		t.Fatalf("discordError() = %v", err)
	}
	if isPermanentActivityError(response.discordError()) {
		t.Fatal("IPC code 4001 classified as a permanent activity rejection")
	}
	response.Data.Code = 4000
	response.Data.Message = "localized or changed validation prose"
	if !isPermanentActivityError(response.discordError()) {
		t.Fatal("IPC code 4000 was not classified as a permanent activity rejection")
	}

	started := time.Unix(123, 456000000)
	payload := newSetActivityPayload(Activity{
		Name:           "Codex",
		StartTimestamp: &started,
		LargeImage:     Image{Key: "key", URL: "https://example.test/image.png", Text: "large"},
		SmallImage:     Image{Key: "small", Text: "small text"},
		Buttons:        []Button{{Label: "Docs", URL: "https://example.test"}},
	}, 42, "nonce")
	if payload.Args.Activity.Timestamps.Start != started.UnixMilli() {
		t.Fatalf("timestamp = %d", payload.Args.Activity.Timestamps.Start)
	}
	if payload.Args.Activity.Assets.LargeImage != "https://example.test/image.png" || payload.Args.Activity.Assets.SmallImage != "small" {
		t.Fatalf("assets = %#v", payload.Args.Activity.Assets)
	}
	if len(payload.Args.Activity.Buttons) != 1 {
		t.Fatalf("buttons = %#v", payload.Args.Activity.Buttons)
	}
}

func TestSetActivityRejectsInvalidPayloadBeforeTransport(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	client := &RichClient{conn: clientConn}

	// Button count is not something normalizeActivity trims — it is a structural
	// error the caller has to fix — so local validation must still stop the
	// payload before any bytes reach the transport. (An over-long label is no
	// longer an example of this: since #436 it is trimmed to fit and published.)
	err := client.SetActivity(Activity{
		Buttons: []Button{
			{Label: "One", URL: "https://example.test"},
			{Label: "Two", URL: "https://example.test"},
			{Label: "Three", URL: "https://example.test"},
		},
	})
	if err == nil || !isPermanentActivityError(err) || !strings.Contains(err.Error(), "at most 2 entries") {
		t.Fatalf("SetActivity() error = %v, want permanent local validation error", err)
	}
	if client.conn == nil {
		t.Fatal("local payload validation closed the healthy connection")
	}
}

func TestUUIDShapeAndVersion(t *testing.T) {
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(id, "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Fatalf("UUID shape = %q", id)
	}
	if parts[2][0] != '4' || !strings.ContainsRune("89ab", rune(parts[3][0])) {
		t.Fatalf("UUID version/variant = %q", id)
	}
}
