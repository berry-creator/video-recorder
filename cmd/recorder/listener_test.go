package main

import (
	"errors"
	"net"
	"syscall"
	"testing"
)

type fakeListener struct {
	address net.Addr
}

func (l fakeListener) Accept() (net.Conn, error) { return nil, errors.New("closed") }
func (l fakeListener) Close() error              { return nil }
func (l fakeListener) Addr() net.Addr            { return l.address }

func TestListenWithFallbackIncrementsDefaultPort(t *testing.T) {
	var attempts []string
	listener, address, err := listenWithFallback("127.0.0.1:8800", func(_ string, candidate string) (net.Listener, error) {
		attempts = append(attempts, candidate)
		if candidate != "127.0.0.1:8802" {
			return nil, syscall.EADDRINUSE
		}
		return fakeListener{address: fakeAddr(candidate)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if address != "127.0.0.1:8802" || len(attempts) != 3 {
		t.Fatalf("listenWithFallback() address = %q, attempts = %v", address, attempts)
	}
}

func TestListenWithFallbackDoesNotChangeConfiguredPort(t *testing.T) {
	wantErr := errors.New("unavailable")
	attempts := 0
	_, address, err := listenWithFallback("127.0.0.1:9100", func(_, _ string) (net.Listener, error) {
		attempts++
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) || address != "127.0.0.1:9100" || attempts != 1 {
		t.Fatalf("listenWithFallback() = (%q, %v), attempts = %d", address, err, attempts)
	}
}

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }
