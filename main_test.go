package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const defaultTestTimeout = 5 * time.Second

// listenerHelper is collection of utility function to create listeners.
//
// The main purpose of these helper functions is to avoid trying to get a
// random name/port manually. It prevents collision between listeners.
//
// It also provides a cleanup function.
type listenerHelper struct {
	mu                sync.Mutex
	unixSocketTmpDir  string
	unixSocketCounter int
	listeners         []net.Listener
}

func (lh *listenerHelper) cleanup() {
	for _, l := range lh.listeners {
		l.Close()
	}
	os.RemoveAll(lh.unixSocketTmpDir)
}

// newUnixListener creates a new unix socket listener
//
// This socket is in a temporary directory, and will get cleaned up.
func (lh *listenerHelper) newUnixListener() (net.Listener, error) {
	lh.mu.Lock()
	defer lh.mu.Unlock()

	socketName := fmt.Sprintf("%05d", lh.unixSocketCounter)
	lh.unixSocketCounter++
	socketPath := filepath.Join(lh.unixSocketTmpDir, socketName)

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}

	lh.listeners = append(lh.listeners, l)
	return l, nil
}

// newTCPListner creates a new tcp socket listener.
//
// This listener will be cleaned up at the end
func (lh *listenerHelper) newTCPListener() (net.Listener, error) {
	lh.mu.Lock()
	defer lh.mu.Unlock()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	lh.listeners = append(lh.listeners, l)
	return l, nil
}

// getListenerHelper is a test helper function for generating a new listenerHelper.
//
// It handles errors and cleanup. This is a call-once and forget about function.
func getListenerHelper(t *testing.T) *listenerHelper {
	t.Helper()
	tmpdir, err := os.MkdirTemp(t.TempDir(), "unix.*")
	if err != nil {
		t.Fatalf("could not getListenerHelper(): %v", err)
	}
	lh := &listenerHelper{unixSocketTmpDir: tmpdir}
	t.Cleanup(lh.cleanup)
	return lh
}

func getUnixSocketPath(t *testing.T) string {
	t.Helper()

	tmpdir, err := os.MkdirTemp("", "gotest.*")
	if err != nil {
		t.Fatalf("could not create temporary directory for socket: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(tmpdir)
	})
	return filepath.Join(tmpdir, "socket")
}

func getContext(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	return ctx
}

func TestSimpleTransmission(t *testing.T) {
	ctx := getContext(t, defaultTestTimeout)
	lh := getListenerHelper(t)

	server, err := lh.newTCPListener()
	if err != nil {
		t.Fatalf("could not create tcp listener: %v", err)
	}
	t.Log("server started")

	proxyAddr := getUnixSocketPath(t)
	go run(ctx, proxyAddr, server.Addr().String())

	ch := make(chan string, 0)
	defer close(ch)

	errCh := make(chan error, 0)
	defer close(errCh)

	want := "deadbeef random string"

	go func() { // Send
		conn, err := net.Dial("unix", proxyAddr)
		if err != nil {
			errCh <- err
		}
		defer conn.Close()
		t.Log("client connection established")

		_, err = conn.Write([]byte(want))
		if err != nil {
			errCh <- err
		}
		t.Log("data was sent from the client")
	}()
	go func() { // Receive
		conn, err := server.Accept()
		if err != nil {
			errCh <- err
		}
		t.Log("new connection accepted")

		data, err := io.ReadAll(conn)
		if err != nil {
			errCh <- err
		}
		t.Log("data from client received")

		ch <- string(data)
	}()

	select {
	case got := <-ch:
		if got != want {
			t.Errorf("wrong data received: got %q, want %q", got, want)
		}
	case err := <-errCh:
		t.Fatalf("encountered error: %v", err)
	case <-ctx.Done():
		t.Error("no data received")
	}
}
