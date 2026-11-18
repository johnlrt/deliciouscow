package events_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"

	"github.com/johnlrt/deliciouscow/server/internal/events"
)

func TestKafkaBusProduceWire(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var lenBuf [4]byte
		_, _ = io.ReadFull(conn, lenBuf[:])
		n := binary.BigEndian.Uint32(lenBuf[:])
		req := make([]byte, n)
		_, _ = io.ReadFull(conn, req)
		done <- req
		// Send minimal 8-byte response (length=4, corrID=1)
		_, _ = conn.Write([]byte{0, 0, 0, 4, 0, 0, 0, 1})
	}()

	bus, err := events.NewKafkaBus([]string{ln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	if err := bus.Publish(context.Background(), events.TopicNodeEvents, "node-1", map[string]string{"type": "node.join"}); err != nil {
		t.Fatal(err)
	}
	req := <-done
	// apiKey=0, apiVersion=2
	if binary.BigEndian.Uint16(req[0:2]) != 0 || binary.BigEndian.Uint16(req[2:4]) != 2 {
		t.Fatalf("unexpected kafka header: %v", req[:4])
	}
}
