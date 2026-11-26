package pgproxy

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// client connects pgconn to the proxy over an in-memory pipe, the way Pawabase's driver reaches it through the tunnel.
func client(t *testing.T, databaseURL, secret, password string) (*pgconn.PgConn, error) {
	t.Helper()
	clientEnd, proxyEnd := net.Pipe()
	go func() { _ = Serve(context.Background(), proxyEnd, databaseURL, secret) }()
	cfg, err := pgconn.ParseConfig("postgres://pawabase@127.0.0.1:5432/pawabase?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Password = password
	cfg.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) { return clientEnd, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return pgconn.ConnectConfig(ctx, cfg)
}

func TestAWrongSessionSecretIsRefusedBeforeTheDatabaseIsTouched(t *testing.T) {
	_, err := client(t, "postgres://nobody@127.0.0.1:1/none", "right-secret", "wrong")
	if err == nil || !strings.Contains(err.Error(), "28P01") && !strings.Contains(err.Error(), "does not accept that password") {
		t.Fatalf("want an authentication failure, got %v", err)
	}
}

// Runs against a real database when PAWABASE_CONNECTOR_TEST_DATABASE_URL is set (CI without one skips it).
func TestQueriesPassThroughToTheRealDatabase(t *testing.T) {
	url := os.Getenv("PAWABASE_CONNECTOR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set PAWABASE_CONNECTOR_TEST_DATABASE_URL to run against a real database")
	}
	conn, err := client(t, url, "s3", "s3")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	result := conn.ExecParams(context.Background(), "SELECT $1::int + 1, current_user", [][]byte{[]byte("41")}, nil, nil, nil).Read()
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if string(result.Rows[0][0]) != "42" {
		t.Fatalf("want 42, got %s", result.Rows[0][0])
	}
	if conn.ParameterStatus("server_version") == "" {
		t.Fatal("the database's parameter statuses should reach the client")
	}
}
