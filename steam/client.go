package steam

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/warsmite/gamejanitor/steam/proto"
	goproto "google.golang.org/protobuf/proto"
)

const (
	protocolVersion    = 65581
	cmListURL          = "https://api.steampowered.com/ISteamDirectory/GetCMListForConnect/v1/?cellid=0"
	wsPath             = "/cmsocket/"
	defaultDialTimeout = 15 * time.Second
)

// Message is a decoded Steam CM message with its header and body.
type Message struct {
	EMsg   EMsg
	Header *proto.CMsgProtoBufHeader
	Body   []byte
}

// Client manages a connection to a Steam CM server over WebSocket.
type Client struct {
	log    *slog.Logger
	conn   *websocket.Conn
	mu     sync.Mutex // protects conn writes
	closed atomic.Bool

	sessionID atomic.Int32
	steamID   atomic.Uint64

	nextJobID atomic.Uint64

	// pending tracks inflight request/response pairs keyed by job ID.
	pending   map[uint64]chan *Message
	pendingMu sync.Mutex

	// emsgWaiters tracks one-shot waiters for specific EMsg types (e.g. logon response).
	emsgWaiters   map[EMsg]chan *Message
	emsgWaitersMu sync.Mutex

	heartbeatCancel context.CancelFunc

	// OnMessage is called for unsolicited messages (notifications, logoffs).
	OnMessage func(*Message)
}

// NewClient creates a new Steam CM client.
func NewClient(log *slog.Logger) *Client {
	return &Client{
		log:         log.With("component", "steam_cm"),
		pending:     make(map[uint64]chan *Message),
		emsgWaiters: make(map[EMsg]chan *Message),
	}
}

// Connect discovers a CM server and establishes a WebSocket connection.
func (c *Client) Connect(ctx context.Context) error {
	addr, err := c.discoverCMServer(ctx)
	if err != nil {
		return fmt.Errorf("discover CM server: %w", err)
	}

	c.log.Info("connecting to Steam CM", "addr", addr)

	dialer := websocket.Dialer{
		HandshakeTimeout: defaultDialTimeout,
	}
	conn, _, err := dialer.DialContext(ctx, "wss://"+addr+wsPath, nil)
	if err != nil {
		return fmt.Errorf("websocket dial %s: %w", addr, err)
	}
	c.conn = conn
	c.closed.Store(false)

	go c.readLoop()
	return nil
}

// Close shuts down the connection and cancels the heartbeat.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	if c.heartbeatCancel != nil {
		c.heartbeatCancel()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// SessionID returns the current session ID assigned by the CM.
func (c *Client) SessionID() int32 {
	return c.sessionID.Load()
}

// SteamID returns the SteamID assigned after login.
func (c *Client) SteamIDValue() uint64 {
	return c.steamID.Load()
}

// Send sends a protobuf message to the CM server.
func (c *Client) Send(emsg EMsg, header *proto.CMsgProtoBufHeader, body goproto.Message) error {
	bodyBytes, err := goproto.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	headerBytes, err := goproto.Marshal(header)
	if err != nil {
		return fmt.Errorf("marshal header: %w", err)
	}

	// Wire format: [4 bytes rawEMsg][4 bytes headerLen][header][body]
	rawEMsg := uint32(emsg) | uint32(eMsgProtoMask)
	headerLen := uint32(len(headerBytes))

	buf := make([]byte, 4+4+len(headerBytes)+len(bodyBytes))
	binary.LittleEndian.PutUint32(buf[0:4], rawEMsg)
	binary.LittleEndian.PutUint32(buf[4:8], headerLen)
	copy(buf[8:8+len(headerBytes)], headerBytes)
	copy(buf[8+len(headerBytes):], bodyBytes)

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, buf)
}

// SendAndWait sends a request and waits for the response with matching job ID.
func (c *Client) SendAndWait(ctx context.Context, emsg EMsg, header *proto.CMsgProtoBufHeader, body goproto.Message) (*Message, error) {
	jobID := c.nextJobID.Add(1)
	header.JobidSource = &jobID

	ch := make(chan *Message, 1)
	c.pendingMu.Lock()
	c.pending[jobID] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, jobID)
		c.pendingMu.Unlock()
	}()

	if err := c.Send(emsg, header, body); err != nil {
		return nil, err
	}

	select {
	case msg := <-ch:
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SendServiceMethod sends a unified service method call and waits for the response.
func (c *Client) SendServiceMethod(ctx context.Context, methodName string, body goproto.Message, authenticated bool) (*Message, error) {
	emsg := EMsgServiceMethodCallFromClient
	if !authenticated {
		emsg = EMsgServiceMethodCallFromClientNonAuthed
	}

	header := c.makeHeader()
	header.TargetJobName = &methodName

	return c.SendAndWait(ctx, emsg, header, body)
}

func (c *Client) makeHeader() *proto.CMsgProtoBufHeader {
	sid := c.steamID.Load()
	sessionID := c.sessionID.Load()
	return &proto.CMsgProtoBufHeader{
		Steamid:         &sid,
		ClientSessionid: &sessionID,
	}
}

func (c *Client) readLoop() {
	for {
		if c.closed.Load() {
			return
		}

		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if !c.closed.Load() {
				c.log.Error("CM read error", "error", err)
			}
			return
		}

		msgs, err := c.decodeMessages(data)
		if err != nil {
			c.log.Warn("CM decode error, skipping message", "error", err)
			continue
		}

		for _, msg := range msgs {
			c.dispatch(msg)
		}
	}
}

func (c *Client) decodeMessages(data []byte) ([]*Message, error) {
	msg, err := c.decodeMessage(data)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, nil // non-proto message, skip
	}

	// Handle Multi messages (batched responses)
	if msg.EMsg == EMsgMulti {
		return c.decodeMulti(msg.Body)
	}

	return []*Message{msg}, nil
}

func (c *Client) decodeMessage(data []byte) (*Message, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("message too short: %d bytes", len(data))
	}

	rawEMsg := EMsg(binary.LittleEndian.Uint32(data[0:4]))
	if !rawEMsg.IsProto() {
		// Non-proto messages (e.g. ClientServerList) are safe to skip on WebSocket.
		return nil, nil
	}

	emsg := rawEMsg.Value()
	headerLen := binary.LittleEndian.Uint32(data[4:8])

	if uint32(len(data)) < 8+headerLen {
		return nil, fmt.Errorf("message truncated: need %d header bytes, have %d", headerLen, len(data)-8)
	}

	header := &proto.CMsgProtoBufHeader{}
	if err := goproto.Unmarshal(data[8:8+headerLen], header); err != nil {
		return nil, fmt.Errorf("unmarshal header: %w", err)
	}

	body := data[8+headerLen:]

	return &Message{
		EMsg:   emsg,
		Header: header,
		Body:   body,
	}, nil
}

func (c *Client) decodeMulti(data []byte) ([]*Message, error) {
	multi := &proto.CMsgMulti{}
	if err := goproto.Unmarshal(data, multi); err != nil {
		return nil, fmt.Errorf("unmarshal CMsgMulti: %w", err)
	}

	payload := multi.GetMessageBody()
	if multi.GetSizeUnzipped() > 0 {
		gz, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		decompressed, err := io.ReadAll(gz)
		gz.Close()
		if err != nil {
			return nil, fmt.Errorf("gzip decompress: %w", err)
		}
		payload = decompressed
	}

	var msgs []*Message
	for len(payload) >= 4 {
		subSize := binary.LittleEndian.Uint32(payload[0:4])
		payload = payload[4:]
		if uint32(len(payload)) < subSize {
			return nil, fmt.Errorf("multi sub-message truncated: need %d, have %d", subSize, len(payload))
		}

		msg, err := c.decodeMessage(payload[:subSize])
		if err != nil {
			return nil, fmt.Errorf("decode sub-message: %w", err)
		}
		if msg != nil {
			msgs = append(msgs, msg)
		}
		payload = payload[subSize:]
	}

	return msgs, nil
}

// WaitForEMsg registers a one-shot waiter for a specific EMsg type.
// Returns a channel that receives the first matching message.
func (c *Client) WaitForEMsg(emsg EMsg) chan *Message {
	ch := make(chan *Message, 1)
	c.emsgWaitersMu.Lock()
	c.emsgWaiters[emsg] = ch
	c.emsgWaitersMu.Unlock()
	return ch
}

func (c *Client) dispatch(msg *Message) {
	// Check EMsg waiters first (for logon response, etc.)
	c.emsgWaitersMu.Lock()
	ch, ok := c.emsgWaiters[msg.EMsg]
	if ok {
		delete(c.emsgWaiters, msg.EMsg)
	}
	c.emsgWaitersMu.Unlock()
	if ok {
		select {
		case ch <- msg:
		default:
		}
		return
	}

	// Check if this is a response to a pending request (job ID correlation).
	if msg.Header.GetJobidTarget() != 0 && msg.Header.GetJobidTarget() != ^uint64(0) {
		c.pendingMu.Lock()
		jch, jok := c.pending[msg.Header.GetJobidTarget()]
		c.pendingMu.Unlock()

		if jok {
			select {
			case jch <- msg:
			default:
			}
			return
		}
	}

	if c.OnMessage != nil {
		c.OnMessage(msg)
	}
}

func (c *Client) startHeartbeat(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	c.heartbeatCancel = cancel

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				header := c.makeHeader()
				body := &proto.CMsgClientHeartBeat{}
				if err := c.Send(EMsgClientHeartBeat, header, body); err != nil {
					c.log.Error("heartbeat send failed", "error", err)
					return
				}
			}
		}
	}()
}

// discoverCMServer fetches the Steam CM server list and returns a WebSocket endpoint.
func (c *Client) discoverCMServer(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cmListURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CM list API returned %d", resp.StatusCode)
	}

	var result struct {
		Response struct {
			ServerList []struct {
				Endpoint string `json:"endpoint"`
				Type     string `json:"type"`
			} `json:"serverlist"`
		} `json:"response"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode CM list: %w", err)
	}

	for _, s := range result.Response.ServerList {
		if s.Type == "websockets" {
			return s.Endpoint, nil
		}
	}

	return "", fmt.Errorf("no WebSocket CM servers found in response")
}
