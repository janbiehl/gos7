package gos7

// Copyright 2018 Trung Hieu Le. All rights reserved.
// This software may be modified and distributed under the terms
// of the BSD license. See the LICENSE file for details.
import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestTCPTransporter(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, err = io.Copy(conn, conn)
		if err != nil {
			t.Error(err)
			return
		}
	}()
	client := &tcpTransporter{
		Address:     ln.Addr().String(),
		Timeout:     200 * time.Second,
		IdleTimeout: 100 * time.Millisecond,
	}
	req := []byte{0, 1, 0, 17, 0, 2, 1, 2, 0, 1, 0, 17, 0, 2, 1, 2, 2} //lengh 17, > MinPduSize

	client.tcpConnect() //assume tcp connect to test locally
	rsp, err := client.Send(req)
	if err != nil {
		t.Fatal(err)
	}
	//lth: just compare 7 first byte
	if !bytes.Equal(req, rsp) {
		t.Fatalf("unexpected response: %x", rsp)
	}
	time.Sleep(150 * time.Millisecond)
	// closeIdle runs on the timer goroutine and writes conn under mu; read it
	// under the same lock so the race detector sees the synchronization.
	client.mu.Lock()
	conn := client.conn
	client.mu.Unlock()
	if conn != nil {
		t.Fatalf("connection is not closed: %+v", conn)
	}
}

// fakePeer accepts TCP connections and hands each to serve, which owns it
// until the test ends.
func fakePeer(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		var conns []net.Conn
		defer func() {
			for _, conn := range conns {
				conn.Close()
			}
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conns = append(conns, conn)
			go serve(conn)
		}
	}()
	return ln.Addr().String()
}

// silentListener never answers, like a non-S7 service on port 102: the ISO
// connection request times out.
func silentListener(t *testing.T) string {
	return fakePeer(t, func(net.Conn) {})
}

// isoOnlyListener confirms the ISO connection request and then goes silent:
// the S7 PDU negotiation times out.
func isoOnlyListener(t *testing.T) string {
	return fakePeer(t, func(conn net.Conn) {
		request := make([]byte, len(isoConnectionRequestTelegram))
		if _, err := io.ReadFull(conn, request); err != nil {
			return
		}
		confirm := make([]byte, 22)
		copy(confirm, []byte{3, 0, 0, 22, 17, 0xD0})
		_, _ = conn.Write(confirm)
	})
}

// TestConnectFailureClearsConn: a connect that fails after the dial, at the
// ISO connection or at the PDU negotiation, must leave no connection behind,
// or the next Connect skips the dial and sends on the dead socket.
func TestConnectFailureClearsConn(t *testing.T) {
	peers := map[string]func(*testing.T) string{
		"iso connect":     silentListener,
		"pdu negotiation": isoOnlyListener,
	}
	for name, peer := range peers {
		t.Run(name, func(t *testing.T) {
			handler := NewTCPClientHandler(peer(t), 0, 1)
			handler.Timeout = 50 * time.Millisecond
			handler.IdleTimeout = 0
			defer handler.Close()

			err := handler.Connect()
			if err == nil {
				t.Fatal("connect to a non-S7 peer succeeded")
			}
			if name == "pdu negotiation" && err.Error() != ErrorText(errCliNegotiatingPDU) {
				t.Fatalf("connect failed before the PDU negotiation: %v", err)
			}
			handler.mu.Lock()
			conn := handler.conn
			handler.mu.Unlock()
			if conn != nil {
				t.Fatalf("connection left set after a failed connect: %+v", conn)
			}
		})
	}
}

// TestConnectFailureRacesIdleClose: with Timeout equal to IdleTimeout, the
// idle timer fires just as connect() gives up on the ISO connection request.
// The failure path must close the connection under the same lock closeIdle
// takes, or `go test -race` reports a data race on conn. The race detector
// only catches an unlucky interleaving, so run many connects at once.
func TestConnectFailureRacesIdleClose(t *testing.T) {
	address := silentListener(t)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler := NewTCPClientHandler(address, 0, 1)
			handler.Timeout = 50 * time.Millisecond
			handler.IdleTimeout = 50 * time.Millisecond

			if err := handler.Connect(); err == nil {
				t.Error("connect to a silent peer succeeded")
			}
			// Let closeIdle run before the explicit Close orders it.
			time.Sleep(100 * time.Millisecond)
			handler.Close()
		}()
	}
	wg.Wait()
}
