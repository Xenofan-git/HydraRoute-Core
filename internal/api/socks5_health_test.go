package api

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestLocalSOCKS5Greeting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := ln.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		var b [3]byte
		c.SetDeadline(time.Now().Add(time.Second))
		_, _ = c.Read(b[:])
		_, _ = c.Write([]byte{5, 0})
	}()
	if err := probeLocalSOCKS5(context.Background(), ln.Addr().String()); err != nil {
		t.Fatal(err)
	}
	<-done
	if err := probeLocalSOCKS5(context.Background(), "127.0.0.1:1"); err == nil {
		t.Fatal("closed inbound considered healthy")
	}
}
