// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package socks5

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

func socks5Server(listener net.Listener) {
	var server Server
	err := server.Serve(listener)
	if err != nil {
		panic(err)
	}
	listener.Close()
}

func backendServer(listener net.Listener) {
	conn, err := listener.Accept()
	if err != nil {
		panic(err)
	}
	conn.Write([]byte("Test"))
	conn.Close()
	listener.Close()
}

func udpEchoServer(conn net.PacketConn) {
	var buf [1024]byte
	n, addr, err := conn.ReadFrom(buf[:])
	if err != nil {
		panic(err)
	}
	_, err = conn.WriteTo(buf[:n], addr)
	if err != nil {
		panic(err)
	}
	conn.Close()
}

func TestRead(t *testing.T) {
	// backend server which we'll use SOCKS5 to connect to
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	backendServerPort := listener.Addr().(*net.TCPAddr).Port
	go backendServer(listener)

	// SOCKS5 server
	socks5, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	socks5Port := socks5.Addr().(*net.TCPAddr).Port
	go socks5Server(socks5)

	addr := fmt.Sprintf("localhost:%d", socks5Port)
	socksDialer, err := proxy.SOCKS5("tcp", addr, nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}

	addr = fmt.Sprintf("localhost:%d", backendServerPort)
	conn, err := socksDialer.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 4)
	_, err = io.ReadFull(conn, buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf) != "Test" {
		t.Fatalf("got: %q want: Test", buf)
	}

	err = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadPassword(t *testing.T) {
	// backend server which we'll use SOCKS5 to connect to
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	backendServerPort := ln.Addr().(*net.TCPAddr).Port
	go backendServer(ln)

	socks5ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		socks5ln.Close()
	})
	auth := &proxy.Auth{User: "foo", Password: "bar"}
	go func() {
		s := Server{Username: auth.User, Password: auth.Password}
		err := s.Serve(socks5ln)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			panic(err)
		}
	}()

	addr := fmt.Sprintf("localhost:%d", socks5ln.Addr().(*net.TCPAddr).Port)

	if d, err := proxy.SOCKS5("tcp", addr, nil, proxy.Direct); err != nil {
		t.Fatal(err)
	} else {
		if _, err := d.Dial("tcp", addr); err == nil {
			t.Fatal("expected no-auth dial error")
		}
	}

	badPwd := &proxy.Auth{User: "foo", Password: "not right"}
	if d, err := proxy.SOCKS5("tcp", addr, badPwd, proxy.Direct); err != nil {
		t.Fatal(err)
	} else {
		if _, err := d.Dial("tcp", addr); err == nil {
			t.Fatal("expected bad password dial error")
		}
	}

	badUsr := &proxy.Auth{User: "not right", Password: "bar"}
	if d, err := proxy.SOCKS5("tcp", addr, badUsr, proxy.Direct); err != nil {
		t.Fatal(err)
	} else {
		if _, err := d.Dial("tcp", addr); err == nil {
			t.Fatal("expected bad username dial error")
		}
	}

	socksDialer, err := proxy.SOCKS5("tcp", addr, auth, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}

	addr = fmt.Sprintf("localhost:%d", backendServerPort)
	conn, err := socksDialer.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "Test" {
		t.Fatalf("got: %q want: Test", buf)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUDP(t *testing.T) {
	// backend UDP server which we'll use SOCKS5 to connect to
	newUDPEchoServer := func() net.PacketConn {
		listener, err := net.ListenPacket("udp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		go udpEchoServer(listener)
		return listener
	}

	const echoServerNumber = 3
	echoServerListener := make([]net.PacketConn, echoServerNumber)
	for i := range echoServerNumber {
		echoServerListener[i] = newUDPEchoServer()
	}
	defer func() {
		for i := range echoServerNumber {
			_ = echoServerListener[i].Close()
		}
	}()

	// SOCKS5 server
	socks5, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	socks5Port := socks5.Addr().(*net.TCPAddr).Port
	go socks5Server(socks5)

	// make a socks5 udpAssociate conn
	newUdpAssociateConn := func() (socks5Conn net.Conn, socks5UDPAddr socksAddr) {
		// net/proxy don't support UDP, so we need to manually send the SOCKS5 UDP request
		conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", socks5Port))
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.Write([]byte{socks5Version, 0x01, noAuthRequired}) // client hello with no auth
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 1024)
		n, err := conn.Read(buf) // server hello
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 || buf[0] != socks5Version || buf[1] != noAuthRequired {
			t.Fatalf("got: %q want: 0x05 0x00", buf[:n])
		}

		targetAddr := socksAddr{addrType: ipv4, addr: "0.0.0.0", port: 0}
		targetAddrPkt, err := targetAddr.marshal()
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.Write(append([]byte{socks5Version, byte(udpAssociate), 0x00}, targetAddrPkt...)) // client request
		if err != nil {
			t.Fatal(err)
		}

		n, err = conn.Read(buf) // server response
		if err != nil {
			t.Fatal(err)
		}
		if n < 3 || !bytes.Equal(buf[:3], []byte{socks5Version, 0x00, 0x00}) {
			t.Fatalf("got: %q want: 0x05 0x00 0x00", buf[:n])
		}
		udpProxySocksAddr, err := parseSocksAddr(bytes.NewReader(buf[3:n]))
		if err != nil {
			t.Fatal(err)
		}

		return conn, udpProxySocksAddr
	}

	conn, udpProxySocksAddr := newUdpAssociateConn()
	defer conn.Close()

	sendUDPAndWaitResponse := func(socks5UDPConn net.Conn, addr socksAddr, body []byte) (responseBody []byte) {
		udpPayload, err := (&udpRequest{addr: addr}).marshal()
		if err != nil {
			t.Fatal(err)
		}
		udpPayload = append(udpPayload, body...)
		_, err = socks5UDPConn.Write(udpPayload)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 1024)
		n, err := socks5UDPConn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		_, responseBody, err = parseUDPRequest(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		return responseBody
	}

	udpProxyAddr, err := net.ResolveUDPAddr("udp", udpProxySocksAddr.hostPort())
	if err != nil {
		t.Fatal(err)
	}
	socks5UDPConn, err := net.DialUDP("udp", nil, udpProxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer socks5UDPConn.Close()

	for i := range echoServerNumber {
		port := echoServerListener[i].LocalAddr().(*net.UDPAddr).Port
		addr := socksAddr{addrType: ipv4, addr: "127.0.0.1", port: uint16(port)}
		requestBody := fmt.Appendf(nil, "Test %d", i)
		responseBody := sendUDPAndWaitResponse(socks5UDPConn, addr, requestBody)
		if !bytes.Equal(requestBody, responseBody) {
			t.Fatalf("got: %q want: %q", responseBody, requestBody)
		}
	}
}

func TestDialTimeoutDefault(t *testing.T) {
	var s Server
	if got := s.dialTimeout(); got != defaultDialTimeout {
		t.Errorf("unset dialTimeout = %v, want default %v", got, defaultDialTimeout)
	}
	s.DialTimeout = 90 * time.Second
	if got := s.dialTimeout(); got != 90*time.Second {
		t.Errorf("dialTimeout = %v, want 90s", got)
	}
	s.DialTimeout = -5 * time.Second
	if got := s.dialTimeout(); got != defaultDialTimeout {
		t.Errorf("negative dialTimeout = %v, want default %v", got, defaultDialTimeout)
	}
}

// TestConnectUsesDialTimeout verifies a CONNECT request dials the backend with
// a context whose deadline reflects Server.DialTimeout (the configurable budget
// that replaced the old hard-coded 5s).
func TestConnectUsesDialTimeout(t *testing.T) {
	const want = 1234 * time.Millisecond
	deadlineCh := make(chan time.Duration, 1)
	srv := &Server{
		DialTimeout: want,
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dl, ok := ctx.Deadline()
			if !ok {
				deadlineCh <- 0
			} else {
				deadlineCh <- time.Until(dl)
			}
			// Fail the dial; we only care about the context deadline.
			return nil, errors.New("test: refusing dial")
		},
	}
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)

	d, err := proxy.SOCKS5("tcp", ln.Addr().String(), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	// Expected to fail (backend dialer returns an error), but it drives handleTCP.
	if c, err := d.Dial("tcp", "example.com:80"); err == nil {
		c.Close()
		t.Fatal("expected SOCKS5 dial to fail")
	}

	select {
	case got := <-deadlineCh:
		if got <= 0 {
			t.Fatal("dial context had no deadline")
		}
		// The remaining time should be close to want (minus protocol handshake).
		if got < want/2 || got > want {
			t.Errorf("dial context deadline ~%v remaining, want close to %v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backend dialer was never called")
	}
}
