package grpclistener

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestObserveResetsLogsPeerReset(t *testing.T) {
	buf := captureLogs(t)
	err := readOne(t, func(conn *net.TCPConn) {
		if err := conn.SetLinger(0); err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, syscall.ECONNRESET) && !errors.Is(err, io.EOF) {
		t.Fatalf("read error = %v", err)
	}
	if errors.Is(err, io.EOF) {
		t.Skip("platform closed the socket with EOF instead of reset")
	}
	text := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("grpc_conn_end")) {
		t.Fatalf("missing grpc_conn_end log: %s", text)
	}
	if !bytes.Contains(buf.Bytes(), []byte("connection reset by peer")) && !bytes.Contains(buf.Bytes(), []byte("ECONNRESET")) {
		t.Fatalf("log missing reset reason: %s", text)
	}
}

func TestObserveResetsIgnoresNormalEOF(t *testing.T) {
	buf := captureLogs(t)
	err := readOne(t, func(conn *net.TCPConn) {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("read error = %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte("grpc_conn_end")) {
		t.Fatalf("normal close was logged: %s", buf.String())
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func readOne(t *testing.T, closeClient func(*net.TCPConn)) error {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	wrapped := ObserveResets(ln)
	errCh := make(chan error, 1)
	go func() {
		conn, acceptErr := wrapped.Accept()
		if acceptErr != nil {
			errCh <- acceptErr
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, readErr := conn.Read(make([]byte, 1))
		errCh <- readErr
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	closeClient(client.(*net.TCPConn))
	return <-errCh
}
