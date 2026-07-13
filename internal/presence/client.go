package presence

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sync"
)

const (
	opcodeHandshake uint32 = 0
	opcodeFrame     uint32 = 1
	maxPayload             = 16 << 20
)

// Client is the Discord IPC boundary. Tests should inject a fake implementation.
type Client interface {
	Login(appID string) error
	SetActivity(Activity) error
	Logout() error
}

// RichClient sends Rich Presence activities over Discord's local IPC transport.
// Its zero value is ready to use.
type RichClient struct {
	mu   sync.Mutex
	conn net.Conn
}

var _ Client = (*RichClient)(nil)

// Login connects to Discord IPC using the public application ID and waits for READY.
func (c *RichClient) Login(appID string) error {
	if appID == "" {
		return errors.New("presence: Discord application ID is required")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}

	conn, err := dialDiscordIPC()
	if err != nil {
		return err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = conn.Close()
		}
	}()

	handshake := handshakePayload{
		Version:  1,
		ClientID: appID,
	}
	if err := writeJSONFrame(conn, opcodeHandshake, handshake); err != nil {
		return fmt.Errorf("presence: send Discord IPC handshake: %w", err)
	}

	frame, err := readFrame(conn)
	if err != nil {
		return fmt.Errorf("presence: read Discord IPC handshake: %w", err)
	}
	if frame.opcode != opcodeFrame {
		return fmt.Errorf("presence: unexpected Discord IPC handshake opcode %d", frame.opcode)
	}
	response, err := parseResponse(frame.payload)
	if err != nil {
		return fmt.Errorf("presence: decode Discord IPC handshake: %w", err)
	}
	if err := response.discordError(); err != nil {
		return err
	}
	if response.Event != "READY" {
		return fmt.Errorf("presence: unexpected Discord IPC handshake event %q", response.Event)
	}

	c.conn = conn
	closeOnError = false
	return nil
}

// SetActivity pushes one activity payload to Discord and waits for its response.
func (c *RichClient) SetActivity(activity Activity) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return errors.New("presence: Discord IPC is not connected")
	}
	successful := false
	defer func() {
		if !successful {
			_ = c.conn.Close()
			c.conn = nil
		}
	}()

	nonce, err := newUUID()
	if err != nil {
		return fmt.Errorf("presence: generate SET_ACTIVITY nonce: %w", err)
	}
	payload := newSetActivityPayload(activity, os.Getpid(), nonce)
	if err := writeJSONFrame(c.conn, opcodeFrame, payload); err != nil {
		return fmt.Errorf("presence: send SET_ACTIVITY: %w", err)
	}

	frame, err := readFrame(c.conn)
	if err != nil {
		return fmt.Errorf("presence: read SET_ACTIVITY response: %w", err)
	}
	if frame.opcode != opcodeFrame {
		return fmt.Errorf("presence: unexpected SET_ACTIVITY response opcode %d", frame.opcode)
	}
	response, err := parseResponse(frame.payload)
	if err != nil {
		return fmt.Errorf("presence: decode SET_ACTIVITY response: %w", err)
	}
	if err := response.discordError(); err != nil {
		return err
	}
	successful = true
	return nil
}

// Logout closes the Discord IPC connection. It is safe when not connected.
func (c *RichClient) Logout() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// Probe checks whether Discord IPC is reachable without setting an activity.
func Probe(appID string) error {
	return probeWith(&RichClient{}, appID)
}

func probeWith(client Client, appID string) error {
	if appID == "" {
		appID = DefaultAppID
	}
	if err := client.Login(appID); err != nil {
		return err
	}
	return client.Logout()
}

type handshakePayload struct {
	Version  int    `json:"v"`
	ClientID string `json:"client_id"`
}

type setActivityPayload struct {
	Command string          `json:"cmd"`
	Nonce   string          `json:"nonce"`
	Args    setActivityArgs `json:"args"`
}

type setActivityArgs struct {
	PID      int             `json:"pid"`
	Activity activityPayload `json:"activity"`
}

type activityPayload struct {
	Name       string             `json:"name,omitempty"`
	Type       int                `json:"type"`
	Details    string             `json:"details,omitempty"`
	State      string             `json:"state,omitempty"`
	Timestamps *timestampsPayload `json:"timestamps,omitempty"`
	Assets     *assetsPayload     `json:"assets,omitempty"`
	Buttons    []buttonPayload    `json:"buttons,omitempty"`
}

type timestampsPayload struct {
	Start int64 `json:"start"`
}

type assetsPayload struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
	SmallImage string `json:"small_image,omitempty"`
	SmallText  string `json:"small_text,omitempty"`
}

type buttonPayload struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

func newSetActivityPayload(activity Activity, pid int, nonce string) setActivityPayload {
	payload := setActivityPayload{
		Command: "SET_ACTIVITY",
		Nonce:   nonce,
		Args: setActivityArgs{
			PID: pid,
			Activity: activityPayload{
				Name:    activity.Name,
				Type:    0,
				Details: activity.Details,
				State:   activity.State,
			},
		},
	}

	if activity.StartTimestamp != nil {
		payload.Args.Activity.Timestamps = &timestampsPayload{
			Start: activity.StartTimestamp.UnixMilli(),
		}
	}

	largeImage := imageValue(activity.LargeImage)
	smallImage := imageValue(activity.SmallImage)
	if largeImage != "" || smallImage != "" {
		assets := &assetsPayload{
			LargeImage: largeImage,
			SmallImage: smallImage,
		}
		if largeImage != "" {
			assets.LargeText = activity.LargeImage.Text
		}
		if smallImage != "" {
			assets.SmallText = activity.SmallImage.Text
		}
		payload.Args.Activity.Assets = assets
	}

	if len(activity.Buttons) > 0 {
		payload.Args.Activity.Buttons = make([]buttonPayload, 0, len(activity.Buttons))
		for _, button := range activity.Buttons {
			payload.Args.Activity.Buttons = append(payload.Args.Activity.Buttons, buttonPayload{
				Label: button.Label,
				URL:   button.URL,
			})
		}
	}

	return payload
}

func imageValue(image Image) string {
	if image.URL != "" {
		return image.URL
	}
	return image.Key
}

type ipcResponse struct {
	Event string `json:"evt"`
	Data  struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"data"`
}

func parseResponse(payload []byte) (ipcResponse, error) {
	var response ipcResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return ipcResponse{}, err
	}
	return response, nil
}

func (r ipcResponse) discordError() error {
	if r.Event != "ERROR" {
		return nil
	}
	return fmt.Errorf("presence: Discord IPC error %d: %s", r.Data.Code, r.Data.Message)
}

type ipcFrame struct {
	opcode  uint32
	payload []byte
}

func writeJSONFrame(writer io.Writer, opcode uint32, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	if len(payload) > math.MaxUint32 {
		return fmt.Errorf("JSON payload is too large: %d bytes", len(payload))
	}

	frame := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(frame[0:4], opcode)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	for len(frame) > 0 {
		written, err := writer.Write(frame)
		if err != nil {
			return fmt.Errorf("write frame: %w", err)
		}
		if written == 0 {
			return fmt.Errorf("write frame: %w", io.ErrShortWrite)
		}
		frame = frame[written:]
	}
	return nil
}

func readFrame(reader io.Reader) (ipcFrame, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(reader, header); err != nil {
		return ipcFrame{}, fmt.Errorf("read frame header: %w", err)
	}

	opcode := binary.LittleEndian.Uint32(header[0:4])
	length := binary.LittleEndian.Uint32(header[4:8])
	if length > maxPayload {
		return ipcFrame{}, fmt.Errorf("refusing frame with %d-byte payload (limit %d)", length, maxPayload)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return ipcFrame{}, fmt.Errorf("read %d-byte frame payload: %w", length, err)
	}
	return ipcFrame{opcode: opcode, payload: payload}, nil
}

func newUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), nil
}
