package sshx

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSilentHandshakeHonorsCancellation(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err == nil {
			defer conn.Close()
			<-ctx.Done()
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	start := time.Now()
	_, err = Run(ctx, Target{Addr: host, Port: p, User: "test"}, string(pem.EncodeToMemory(block)), "", "uptime", 100)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("handshake did not stop: %v", err)
	}
	<-done
}

func TestConcurrentOutputRespectsLimit(t *testing.T) {
	var out bytes.Buffer
	truncated := false
	w := &limitedWriter{buf: &out, limit: 1024, truncated: &truncated}
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for n := 0; n < 100; n++ {
				_, _ = w.Write([]byte("stdout/stderr"))
			}
		}()
	}
	workers.Wait()
	if out.Len() != 1024 || !truncated {
		t.Fatalf("output limit: %d %v", out.Len(), truncated)
	}
}
