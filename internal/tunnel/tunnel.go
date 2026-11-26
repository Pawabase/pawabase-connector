// Package tunnel keeps the connector's one outbound WebSocket to Pawabase and runs a Postgres proxy for every stream on it.
//
// Frames (binary): 1 byte type (1 OPEN, 2 DATA, 3 CLOSE), 4 bytes stream id (big-endian), payload. Control (text, JSON): Pawabase says
// "hello" with the session secret; the connector checks its database and answers "ready" (or "error"); "ping"/"pong" either way.
package tunnel

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Pawabase/pawabase-connector/internal/config"
	"github.com/Pawabase/pawabase-connector/internal/pgproxy"
)

const (
	frameOpen  = 1
	frameData  = 2
	frameClose = 3
	// chunk is the most bytes of one stream sent in one frame.
	chunk = 32 * 1024
	// streamBuffer is how many frames may wait for a slow stream before the tunnel waits for it.
	streamBuffer = 256
)

// Version is the connector's version, set at build time.
var Version = "dev"

// ErrRevoked means Pawabase no longer accepts this connector's key: it was removed in Studio, or the key is wrong.
var ErrRevoked = errors.New("Pawabase refused this connector's key: it was removed or replaced in Studio. Enroll a new one")

// Run keeps the tunnel up until ctx ends, reconnecting with backoff. It returns only when ctx ends or the key is refused.
func Run(ctx context.Context, cfg *config.Config, logger *log.Logger) error {
	backoff := time.Second
	for {
		started := time.Now()
		err := session(ctx, cfg, logger)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrRevoked) {
			return err
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		logger.Printf("tunnel down: %v (retrying in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// TunnelURL is the WebSocket address of Pawabase's tunnel endpoint.
func TunnelURL(server string) string {
	u := strings.TrimRight(server, "/") + "/connect/v1/tunnel"
	u = strings.Replace(u, "https://", "wss://", 1)
	return strings.Replace(u, "http://", "ws://", 1)
}

type conn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	streams sync.Map // uint32 -> *stream
	secret  string
	cfg     *config.Config
	logger  *log.Logger
}

func session(ctx context.Context, cfg *config.Config, logger *log.Logger) error {
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+cfg.Key)
	header.Set("User-Agent", "pawabase-connector/"+Version)
	ws, resp, err := websocket.Dial(dialCtx, TunnelURL(cfg.Server), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return ErrRevoked
		}
		return fmt.Errorf("connect to %s: %w", cfg.Server, err)
	}
	ws.SetReadLimit(4 << 20)
	c := &conn{ws: ws, cfg: cfg, logger: logger}
	defer c.closeAll()
	defer ws.CloseNow()

	sessionCtx, stop := context.WithCancel(ctx)
	defer stop()
	go c.keepalive(sessionCtx)

	for {
		kind, data, err := ws.Read(sessionCtx)
		if err != nil {
			switch websocket.CloseStatus(err) {
			case 4401:
				return ErrRevoked
			case 4409:
				return errors.New("another copy of this connector took over the tunnel")
			}
			return err
		}
		if kind == websocket.MessageText {
			if err := c.control(sessionCtx, data); err != nil {
				return err
			}
			continue
		}
		if len(data) < 5 {
			return errors.New("Pawabase sent a malformed frame")
		}
		frameType, id, payload := data[0], binary.BigEndian.Uint32(data[1:5]), data[5:]
		switch frameType {
		case frameOpen:
			if c.secret == "" {
				c.sendFrame(sessionCtx, frameClose, id, nil)
				continue
			}
			s := newStream(c, id)
			c.streams.Store(id, s)
			go func() {
				if err := pgproxy.Serve(sessionCtx, s, cfg.DatabaseURL, c.secret); err != nil && sessionCtx.Err() == nil {
					logger.Printf("stream %d: %v", id, err)
				}
			}()
		case frameData:
			if v, ok := c.streams.Load(id); ok {
				v.(*stream).deliver(sessionCtx, payload)
			}
		case frameClose:
			if v, ok := c.streams.LoadAndDelete(id); ok {
				v.(*stream).remoteClosed()
			}
		}
	}
}

func (c *conn) control(ctx context.Context, data []byte) error {
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil
	}
	switch msg["type"] {
	case "hello":
		secret, _ := msg["secret"].(string)
		if secret == "" {
			return errors.New("Pawabase sent no session secret")
		}
		c.secret = secret
		info, err := Check(ctx, c.cfg.DatabaseURL)
		if err != nil {
			c.sendJSON(ctx, map[string]any{"type": "error", "message": err.Error()})
			return fmt.Errorf("database: %w", err)
		}
		info["type"] = "ready"
		c.sendJSON(ctx, info)
		c.logger.Printf("connected to %s; serving %s as %s (%s)", c.cfg.Server, info["database"], info["user"], info["server_version"])
	case "ping":
		c.sendJSON(ctx, map[string]any{"type": "pong"})
	}
	return nil
}

// Check logs into the database once and says what it found: what Pawabase shows in Studio.
func Check(ctx context.Context, databaseURL string) (map[string]any, error) {
	if databaseURL == "" {
		return nil, errors.New("no database address: set it with `pawabase-connector enroll` or DATABASE_URL")
	}
	checkCtx, cancel := context.WithTimeout(ctx, pgproxy.ConnectTimeout)
	defer cancel()
	db, err := pgconn.Connect(checkCtx, databaseURL)
	if err != nil {
		return nil, err
	}
	defer db.Close(context.Background())
	result := db.ExecParams(checkCtx, "SELECT current_database(), current_user, current_setting('server_version')", nil, nil, nil, nil).Read()
	if result.Err != nil {
		return nil, result.Err
	}
	row := result.Rows[0]
	host, _ := os.Hostname()
	return map[string]any{
		"version": Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "hostname": host,
		"database": string(row[0]), "user": string(row[1]), "server_version": string(row[2]),
		"ssl": fmt.Sprint(isTLS(db)),
	}, nil
}

func (c *conn) keepalive(ctx context.Context) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				c.ws.Close(websocket.StatusGoingAway, "no answer to ping")
				return
			}
		}
	}
}

func (c *conn) sendJSON(ctx context.Context, msg map[string]any) {
	raw, _ := json.Marshal(msg)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.Write(ctx, websocket.MessageText, raw)
}

func (c *conn) sendFrame(ctx context.Context, kind byte, id uint32, payload []byte) error {
	buf := make([]byte, 5+len(payload))
	buf[0] = kind
	binary.BigEndian.PutUint32(buf[1:5], id)
	copy(buf[5:], payload)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageBinary, buf)
}

func (c *conn) closeAll() {
	c.streams.Range(func(key, value any) bool {
		value.(*stream).remoteClosed()
		c.streams.Delete(key)
		return true
	})
}

// stream is one Postgres connection carried by the tunnel, seen by the proxy as an ordinary connection.
type stream struct {
	c       *conn
	id      uint32
	in      chan []byte
	pending []byte
	once    sync.Once
	done    chan struct{}
}

func newStream(c *conn, id uint32) *stream {
	return &stream{c: c, id: id, in: make(chan []byte, streamBuffer), done: make(chan struct{})}
}

func (s *stream) deliver(ctx context.Context, payload []byte) {
	data := append([]byte(nil), payload...)
	select {
	case s.in <- data:
	case <-s.done:
	case <-ctx.Done():
	}
}

func (s *stream) Read(p []byte) (int, error) {
	if len(s.pending) == 0 {
		select {
		case data, ok := <-s.in:
			if !ok {
				return 0, io.EOF
			}
			s.pending = data
		case <-s.done:
			select {
			case data := <-s.in:
				s.pending = data
			default:
				return 0, io.EOF
			}
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *stream) Write(p []byte) (int, error) {
	select {
	case <-s.done:
		return 0, io.ErrClosedPipe
	default:
	}
	written := 0
	for len(p) > 0 {
		n := min(len(p), chunk)
		if err := s.c.sendFrame(context.Background(), frameData, s.id, p[:n]); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

// Close ends the stream from this side and tells Pawabase.
func (s *stream) Close() error {
	s.once.Do(func() {
		close(s.done)
		if _, ok := s.c.streams.LoadAndDelete(s.id); ok {
			_ = s.c.sendFrame(context.Background(), frameClose, s.id, nil)
		}
	})
	return nil
}

func (s *stream) remoteClosed() {
	s.once.Do(func() { close(s.done) })
}

func isTLS(db *pgconn.PgConn) bool {
	_, ok := db.Conn().(*tls.Conn)
	return ok
}
