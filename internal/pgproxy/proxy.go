// Package pgproxy answers one Postgres client (a stream from Pawabase) as a server, then hands it a real connection to the database.
//
// The client must give the tunnel's session secret as its password. The connector then logs into the real database itself, with the
// address only it holds, replays what the database said about itself (parameter statuses, the backend key, ready), and from then on
// copies bytes both ways. Pawabase never sees the database's address or password.
package pgproxy

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// ConnectTimeout bounds how long logging into the real database may take.
const ConnectTimeout = 15 * time.Second

// Startup parameters a client may not choose: who it is and which database come from the connector's own address.
var ignored = map[string]bool{"user": true, "database": true, "password": true, "replication": true, "options": true}

// Serve runs the conversation with one client until either side ends. secret is the password the client must give.
func Serve(ctx context.Context, client io.ReadWriteCloser, databaseURL, secret string) error {
	defer client.Close()
	backend := pgproto3.NewBackend(client, client)

	var startup *pgproto3.StartupMessage
	for startup == nil {
		msg, err := backend.ReceiveStartupMessage()
		if err != nil {
			return fmt.Errorf("startup: %w", err)
		}
		switch m := msg.(type) {
		case *pgproto3.SSLRequest, *pgproto3.GSSEncRequest:
			// The tunnel is already encrypted end to end; the client's leg is a local socket at Pawabase.
			if _, err := client.Write([]byte{'N'}); err != nil {
				return err
			}
		case *pgproto3.CancelRequest:
			return forwardCancel(ctx, databaseURL, m)
		case *pgproto3.StartupMessage:
			startup = m
		default:
			return fmt.Errorf("unexpected startup message %T", msg)
		}
	}

	if err := authenticate(backend, secret); err != nil {
		return err
	}

	cfg, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		refuse(backend, "08000", "the connector's database address is not valid: "+err.Error())
		return err
	}
	for k, v := range startup.Parameters {
		if !ignored[k] {
			cfg.RuntimeParams[k] = v
		}
	}
	connectCtx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()
	conn, err := pgconn.ConnectConfig(connectCtx, cfg)
	if err != nil {
		refuse(backend, "08006", "the connector could not reach its database: "+err.Error())
		return err
	}
	hijacked, err := conn.Hijack()
	if err != nil {
		conn.Close(ctx)
		refuse(backend, "08006", "the connector could not take over the database connection")
		return err
	}
	db := hijacked.Conn
	defer db.Close()

	backend.Send(&pgproto3.AuthenticationOk{})
	keys := make([]string, 0, len(hijacked.ParameterStatuses))
	for k := range hijacked.ParameterStatuses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		backend.Send(&pgproto3.ParameterStatus{Name: k, Value: hijacked.ParameterStatuses[k]})
	}
	backend.Send(&pgproto3.BackendKeyData{ProcessID: hijacked.PID, SecretKey: hijacked.SecretKey})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := backend.Flush(); err != nil {
		return err
	}
	return pipe(client, db)
}

// authenticate asks for the session secret in clear text (the client's leg never leaves Pawabase's host) and checks it.
func authenticate(backend *pgproto3.Backend, secret string) error {
	backend.Send(&pgproto3.AuthenticationCleartextPassword{})
	if err := backend.Flush(); err != nil {
		return err
	}
	if err := backend.SetAuthType(pgproto3.AuthTypeCleartextPassword); err != nil {
		return err
	}
	msg, err := backend.Receive()
	if err != nil {
		return fmt.Errorf("password: %w", err)
	}
	password, ok := msg.(*pgproto3.PasswordMessage)
	if !ok || subtle.ConstantTimeCompare([]byte(password.Password), []byte(secret)) != 1 {
		refuse(backend, "28P01", "this tunnel session does not accept that password")
		return errors.New("a client gave the wrong session secret")
	}
	return nil
}

func refuse(backend *pgproto3.Backend, code, message string) {
	backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: code, Message: message})
	_ = backend.Flush()
}

// forwardCancel passes a client's cancel request to the database: the backend key the client holds is the real one.
func forwardCancel(ctx context.Context, databaseURL string, m *pgproto3.CancelRequest) error {
	cfg, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		return err
	}
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	raw, err := m.Encode(nil)
	if err != nil {
		return err
	}
	_, err = conn.Write(raw)
	return err
}

// pipe copies bytes both ways until one side closes.
func pipe(a io.ReadWriteCloser, b net.Conn) error {
	done := make(chan error, 2)
	go func() { _, err := io.Copy(b, a); done <- err }()
	go func() { _, err := io.Copy(a, b); done <- err }()
	err := <-done
	_ = a.Close()
	_ = b.Close()
	<-done
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	return err
}
