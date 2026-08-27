package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type Transport string

const (
	TransportWS  Transport = "ws"
	TransportSSE Transport = "sse"
)

type PushAckResult struct {
	OK        bool   `json:"ok"`
	UserID    string `json:"user_id"`
	Acked     int    `json:"acked"`
	Total     int    `json:"total"`
	Delivered int    `json:"delivered"`
	Detail    string `json:"detail,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Stats struct {
	Transport             string         `json:"transport"`
	MaxConnectionsPerUser int            `json:"max_connections_per_user"`
	Users                 int            `json:"users"`
	TotalConnections      int            `json:"total_connections"`
	ConnectionsByUser     map[string]int `json:"connections_by_user"`
}

type InboundMessage struct {
	UserID     string          `json:"user_id"`
	ClientID   string          `json:"client_id"`
	Transport  Transport       `json:"transport"`
	Payload    map[string]any  `json:"payload"`
	Raw        json.RawMessage `json:"raw"`
	ReceivedAt time.Time       `json:"received_at"`
}

type Hub struct {
	transport             Transport
	maxConnectionsPerUser int
	ackTimeout            time.Duration
	ackMaxRetries         int

	upgrader websocket.Upgrader

	mu            sync.RWMutex
	clientsByUser map[string][]*client
	clientByID    map[string]*client

	inboundMu   sync.RWMutex
	inboundSubs map[string]func(InboundMessage)
}

type client struct {
	id        string
	userID    string
	transport Transport
	createdAt time.Time

	ws  *wsClient
	sse *sseClient
}

type outbound struct {
	messageType int
	payload     []byte
}

type wsClient struct {
	conn      *websocket.Conn
	send      chan outbound
	closed    chan struct{}
	closeOnce sync.Once

	seqMu sync.Mutex
	seq   int

	ackMu      sync.Mutex
	ackWaiters map[int]chan struct{}
}

type sseClient struct {
	send      chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

const (
	wsReadTimeout   = 45 * time.Second
	wsWriteTimeout  = 10 * time.Second
	wsPingInterval  = 15 * time.Second
	wsAppPingPeriod = 10 * time.Second
)

func NewHub(cfg *config.Config) *Hub {
	transport := TransportWS
	maxConns := 2
	ackTimeout := 10 * time.Second
	ackMaxRetries := 3

	if cfg != nil && cfg.Realtime != nil {
		if strings.EqualFold(cfg.Realtime.Transport, string(TransportSSE)) {
			transport = TransportSSE
		}
		if cfg.Realtime.MaxConnectionsPerUser > 0 {
			maxConns = cfg.Realtime.MaxConnectionsPerUser
		}
		if cfg.Realtime.AckTimeoutSeconds > 0 {
			ackTimeout = time.Duration(cfg.Realtime.AckTimeoutSeconds) * time.Second
		}
		if cfg.Realtime.AckMaxRetries >= 0 {
			ackMaxRetries = cfg.Realtime.AckMaxRetries
		}
	}

	return &Hub{
		transport:             transport,
		maxConnectionsPerUser: maxConns,
		ackTimeout:            ackTimeout,
		ackMaxRetries:         ackMaxRetries,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(_ *http.Request) bool {
				return true
			},
		},
		clientsByUser: make(map[string][]*client),
		clientByID:    make(map[string]*client),
		inboundSubs:   make(map[string]func(InboundMessage)),
	}
}

func (h *Hub) SubscribeInbound(handler func(InboundMessage)) func() {
	if handler == nil {
		return func() {}
	}
	subID := uuid.NewString()
	h.inboundMu.Lock()
	h.inboundSubs[subID] = handler
	h.inboundMu.Unlock()

	return func() {
		h.inboundMu.Lock()
		delete(h.inboundSubs, subID)
		h.inboundMu.Unlock()
	}
}

func (h *Hub) dispatchInbound(msg InboundMessage) {
	h.inboundMu.RLock()
	subs := make([]func(InboundMessage), 0, len(h.inboundSubs))
	for _, handler := range h.inboundSubs {
		subs = append(subs, handler)
	}
	h.inboundMu.RUnlock()

	for _, handler := range subs {
		handler(msg)
	}
}

func (h *Hub) Transport() Transport {
	return h.transport
}

func (h *Hub) MaxConnectionsPerUser() int {
	return h.maxConnectionsPerUser
}

func (h *Hub) Stats() Stats {
	h.mu.RLock()
	defer h.mu.RUnlock()

	connectionsByUser := make(map[string]int, len(h.clientsByUser))
	total := 0
	for userID, clients := range h.clientsByUser {
		connectionsByUser[userID] = len(clients)
		total += len(clients)
	}

	return Stats{
		Transport:             string(h.transport),
		MaxConnectionsPerUser: h.maxConnectionsPerUser,
		Users:                 len(h.clientsByUser),
		TotalConnections:      total,
		ConnectionsByUser:     connectionsByUser,
	}
}

func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request, userID string) error {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}

	ws := &wsClient{
		conn:       conn,
		send:       make(chan outbound, 128),
		closed:     make(chan struct{}),
		ackWaiters: make(map[int]chan struct{}),
	}

	c := &client{
		id:        uuid.NewString(),
		userID:    userID,
		transport: TransportWS,
		createdAt: time.Now(),
		ws:        ws,
	}

	h.registerClient(c)
	defer h.unregisterClient(c.id)

	go h.wsWriteLoop(c)
	h.wsReadLoop(c)

	return nil
}

func (h *Hub) HandleSSE(w http.ResponseWriter, r *http.Request, userID string) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("streaming unsupported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sse := &sseClient{
		send:   make(chan []byte, 128),
		closed: make(chan struct{}),
	}

	c := &client{
		id:        uuid.NewString(),
		userID:    userID,
		transport: TransportSSE,
		createdAt: time.Now(),
		sse:       sse,
	}

	h.registerClient(c)
	defer h.unregisterClient(c.id)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	for {
		select {
		case payload := <-sse.send:
			if err := writeSSEData(w, payload); err != nil {
				return err
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return err
			}
			flusher.Flush()
		case <-r.Context().Done():
			return nil
		case <-sse.closed:
			return nil
		}
	}
}

func (h *Hub) PushMessage(userID string, data any) error {
	if strings.TrimSpace(userID) == "" {
		return errors.New("user_id is required")
	}
	clients := h.snapshotUserClients(userID)
	if len(clients) == 0 {
		return fmt.Errorf("no_client_for_user:%s", userID)
	}

	for _, c := range clients {
		if err := h.sendWithoutAck(c, data); err != nil {
			logger.Warnf(context.Background(), "[Realtime] push message failed user_id=%s client_id=%s err=%v", userID, c.id, err)
			h.unregisterClient(c.id)
		}
	}
	return nil
}

func (h *Hub) PushMessageWaitAck(userID string, data any, timeout time.Duration) PushAckResult {
	result := PushAckResult{UserID: userID}
	if strings.TrimSpace(userID) == "" {
		result.Error = "user_id is required"
		return result
	}
	clients := h.snapshotUserClients(userID)
	if len(clients) == 0 {
		result.Error = "no_client"
		return result
	}
	if timeout <= 0 {
		timeout = h.ackTimeout
	}

	for _, c := range clients {
		if c.transport != TransportWS {
			if err := h.sendWithoutAck(c, data); err == nil {
				result.Delivered++
			} else {
				h.unregisterClient(c.id)
			}
			continue
		}

		result.Total++
		ok, ackErr := h.sendWithAck(c, data, timeout)
		if ackErr != nil {
			h.unregisterClient(c.id)
			continue
		}
		result.Delivered++
		if ok {
			result.Acked++
		}
	}

	if result.Total == 0 {
		result.OK = result.Delivered > 0
		result.Detail = "message_does_not_require_ack"
		return result
	}

	result.OK = result.Acked == result.Total
	if !result.OK {
		result.Detail = "partial_ack"
	}
	return result
}

func (h *Hub) sendWithAck(c *client, data any, timeout time.Duration) (bool, error) {
	if c.ws == nil {
		return false, nil
	}
	message, seq, needAck, err := c.ws.prepareOutbound(data)
	if err != nil {
		return false, err
	}
	if !needAck || seq == 0 {
		return false, c.ws.enqueue(message)
	}

	waiter := c.ws.registerAckWaiter(seq)
	defer c.ws.removeAckWaiter(seq)

	for attempt := 0; attempt <= h.ackMaxRetries; attempt++ {
		if err := c.ws.enqueue(message); err != nil {
			return false, err
		}
		if waitForAck(waiter, timeout) {
			return true, nil
		}
	}

	return false, nil
}

func waitForAck(waiter chan struct{}, timeout time.Duration) bool {
	if timeout <= 0 {
		<-waiter
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-waiter:
		return true
	case <-timer.C:
		return false
	}
}

func (h *Hub) sendWithoutAck(c *client, data any) error {
	switch c.transport {
	case TransportWS:
		if c.ws == nil {
			return errors.New("ws client missing")
		}
		message, _, _, err := c.ws.prepareOutbound(data)
		if err != nil {
			return err
		}
		return c.ws.enqueue(message)
	case TransportSSE:
		if c.sse == nil {
			return errors.New("sse client missing")
		}
		payload, err := marshalPayload(data)
		if err != nil {
			return err
		}
		select {
		case c.sse.send <- payload:
			return nil
		default:
			return errors.New("sse send queue full")
		}
	default:
		return errors.New("unsupported transport")
	}
}

func (h *Hub) registerClient(c *client) {
	var evicted *client
	var totalForUser int

	h.mu.Lock()
	clients := h.clientsByUser[c.userID]
	if len(clients) >= h.maxConnectionsPerUser {
		evicted = clients[0]
		clients = clients[1:]
	}
	clients = append(clients, c)
	h.clientsByUser[c.userID] = clients
	h.clientByID[c.id] = c
	totalForUser = len(clients)
	h.mu.Unlock()

	logger.Infof(context.Background(), "[Realtime] client registered total=%d user_id=%s client_id=%s transport=%s", totalForUser, c.userID, c.id, c.transport)
	if evicted != nil {
		// Reuse the unified unregister path so eviction and normal disconnect
		// share the same cleanup behavior.
		h.unregisterClient(evicted.id)
	}
}

func (h *Hub) unregisterClient(clientID string) {
	h.mu.Lock()
	c, exists := h.clientByID[clientID]
	if !exists {
		h.mu.Unlock()
		return
	}
	delete(h.clientByID, clientID)

	clients := h.clientsByUser[c.userID]
	filtered := make([]*client, 0, len(clients))
	for _, item := range clients {
		if item.id != clientID {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		delete(h.clientsByUser, c.userID)
	} else {
		h.clientsByUser[c.userID] = filtered
	}
	h.mu.Unlock()

	c.close()
}

func (h *Hub) snapshotUserClients(userID string) []*client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	clients := h.clientsByUser[userID]
	out := make([]*client, len(clients))
	copy(out, clients)
	return out
}

func (h *Hub) wsReadLoop(c *client) {
	if c.ws == nil {
		return
	}
	ws := c.ws
	defer ws.close()

	_ = ws.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	ws.conn.SetPongHandler(func(string) error {
		_ = ws.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		return nil
	})

	for {
		messageType, data, err := ws.conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}

		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			continue
		}

		if typ, _ := payload["type"].(string); typ == "ack" {
			if seq, ok := toInt(payload["seq"]); ok {
				ws.ackReceived(seq)
			}
			continue
		}

		if typ, _ := payload["type"].(string); typ == "pong" {
			continue
		}

		if seq, ok := toInt(payload["seq"]); ok {
			ack := map[string]any{"type": "ack", "seq": seq}
			raw, _ := json.Marshal(ack)
			_ = ws.enqueue(outbound{messageType: websocket.TextMessage, payload: raw})
		}

		h.dispatchInbound(InboundMessage{
			UserID:     c.userID,
			ClientID:   c.id,
			Transport:  c.transport,
			Payload:    payload,
			Raw:        append(json.RawMessage(nil), data...),
			ReceivedAt: time.Now(),
		})
	}
}

func (h *Hub) wsWriteLoop(c *client) {
	if c.ws == nil {
		return
	}
	ws := c.ws
	controlTicker := time.NewTicker(wsPingInterval)
	appTicker := time.NewTicker(wsAppPingPeriod)
	defer controlTicker.Stop()
	defer appTicker.Stop()
	defer ws.close()

	for {
		select {
		case msg, ok := <-ws.send:
			if !ok {
				return
			}
			_ = ws.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := ws.conn.WriteMessage(msg.messageType, msg.payload); err != nil {
				return
			}
		case <-controlTicker.C:
			_ = ws.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := ws.conn.WriteMessage(websocket.PingMessage, []byte("ping")); err != nil {
				return
			}
		case <-appTicker.C:
			// App-level ping is visible to browser JS, helping client-side liveness checks.
			payload := map[string]any{"type": "ping", "ts": time.Now().UTC().UnixMilli()}
			raw, err := json.Marshal(payload)
			if err != nil {
				continue
			}
			_ = ws.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := ws.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				return
			}
		case <-ws.closed:
			return
		}
	}
}

func (c *client) close() {
	if c.ws != nil {
		c.ws.close()
	}
	if c.sse != nil {
		c.sse.close()
	}
}

func (c *wsClient) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.conn.Close()

		c.ackMu.Lock()
		for seq, ch := range c.ackWaiters {
			delete(c.ackWaiters, seq)
			close(ch)
		}
		c.ackMu.Unlock()
	})
}

func (c *sseClient) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
	})
}

func (c *wsClient) enqueue(msg outbound) error {
	select {
	case <-c.closed:
		return errors.New("websocket client is closed")
	default:
	}

	select {
	case c.send <- msg:
		return nil
	default:
		return errors.New("websocket send queue full")
	}
}

func (c *wsClient) nextSeq() int {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	c.seq++
	return c.seq
}

func (c *wsClient) registerAckWaiter(seq int) chan struct{} {
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	if ch, ok := c.ackWaiters[seq]; ok {
		return ch
	}
	ch := make(chan struct{}, 1)
	c.ackWaiters[seq] = ch
	return ch
}

func (c *wsClient) removeAckWaiter(seq int) {
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	ch, ok := c.ackWaiters[seq]
	if !ok {
		return
	}
	delete(c.ackWaiters, seq)
	close(ch)
}

func (c *wsClient) ackReceived(seq int) {
	c.ackMu.Lock()
	ch, ok := c.ackWaiters[seq]
	if ok {
		delete(c.ackWaiters, seq)
	}
	c.ackMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
	close(ch)
}

func (c *wsClient) prepareOutbound(data any) (outbound, int, bool, error) {
	msg, seq, needAck, err := buildWSOutboundPayload(data, c.nextSeq)
	if err != nil {
		return outbound{}, 0, false, err
	}
	return msg, seq, needAck, nil
}

func buildWSOutboundPayload(data any, nextSeq func() int) (outbound, int, bool, error) {
	switch v := data.(type) {
	case string:
		var parsed map[string]any
		if err := json.Unmarshal([]byte(v), &parsed); err == nil {
			return buildWSOutboundPayload(parsed, nextSeq)
		}
		return outbound{messageType: websocket.TextMessage, payload: []byte(v)}, 0, false, nil
	case []byte:
		return outbound{messageType: websocket.TextMessage, payload: v}, 0, false, nil
	case map[string]any:
		payload := make(map[string]any, len(v))
		for key, val := range v {
			payload[key] = val
		}

		typ, _ := payload["type"].(string)
		if typ == "ping" || typ == "ack" {
			raw, err := json.Marshal(payload)
			if err != nil {
				return outbound{}, 0, false, err
			}
			return outbound{messageType: websocket.TextMessage, payload: raw}, 0, false, nil
		}

		requireAck := true
		if rawRequireAck, ok := payload["require_ack"]; ok {
			if b, ok := rawRequireAck.(bool); ok {
				requireAck = b
			}
		}

		seq := 0
		if requireAck {
			if rawSeq, ok := toInt(payload["seq"]); ok {
				seq = rawSeq
			} else {
				seq = nextSeq()
				payload["seq"] = seq
			}
			payload["require_ack"] = true
		}

		raw, err := json.Marshal(payload)
		if err != nil {
			return outbound{}, 0, false, err
		}
		return outbound{messageType: websocket.TextMessage, payload: raw}, seq, requireAck, nil
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return outbound{}, 0, false, err
		}
		return outbound{messageType: websocket.TextMessage, payload: raw}, 0, false, nil
	}
}

func marshalPayload(data any) ([]byte, error) {
	switch v := data.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	default:
		return json.Marshal(v)
	}
}

func writeSSEData(w http.ResponseWriter, payload []byte) error {
	content := strings.ReplaceAll(string(payload), "\n", "\ndata: ")
	_, err := w.Write([]byte("data: " + content + "\n\n"))
	return err
}

func toInt(v any) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case float64:
		return int(val), true
	case json.Number:
		i, err := val.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}
