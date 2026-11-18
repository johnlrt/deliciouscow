package events

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	TopicNodeEvents   = "cordova.node.events.v1"
	TopicNodeRank     = "cordova.node.rank.v1"
	TopicAccountUsage = "cordova.account.usage.v1"
	TopicEventsDLQ    = "cordova.events.dlq.v1"
)

type Bus interface {
	Publish(ctx context.Context, topic, key string, payload any) error
	Close()
}

type NoopBus struct{}

func (NoopBus) Publish(_ context.Context, _, _ string, _ any) error { return nil }
func (NoopBus) Close()                                              {}

// KafkaBus implements a lightweight Kafka TCP Produce v2 (uncompressed MessageSet) client
// for C7 event topics without external compression module dependencies.
type KafkaBus struct {
	brokers []string
	mu      sync.Mutex
	conn    net.Conn
	corrID  atomic.Int32
}

func NewKafkaBus(brokers []string) (*KafkaBus, error) {
	if len(brokers) == 0 {
		return nil, errors.New("no kafka brokers configured")
	}
	conn, err := net.DialTimeout("tcp", brokers[0], 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("kafka dial %s: %w", brokers[0], err)
	}
	return &KafkaBus{brokers: brokers, conn: conn}, nil
}

func (k *KafkaBus) Publish(ctx context.Context, topic, key string, payload any) error {
	val, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := k.encodeProduceV2(topic, 0, []byte(key), val)

	k.mu.Lock()
	defer k.mu.Unlock()
	if k.conn == nil {
		conn, err := net.DialTimeout("tcp", k.brokers[0], 3*time.Second)
		if err != nil {
			return err
		}
		k.conn = conn
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = k.conn.SetDeadline(dl)
	} else {
		_ = k.conn.SetDeadline(time.Now().Add(5 * time.Second))
	}
	if _, err := k.conn.Write(frame); err != nil {
		_ = k.conn.Close()
		k.conn = nil
		return err
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(k.conn, lenBuf[:]); err != nil {
		_ = k.conn.Close()
		k.conn = nil
		return err
	}
	respLen := binary.BigEndian.Uint32(lenBuf[:])
	if respLen > 1<<20 {
		return fmt.Errorf("unexpected kafka response size %d", respLen)
	}
	resp := make([]byte, respLen)
	if _, err := io.ReadFull(k.conn, resp); err != nil {
		_ = k.conn.Close()
		k.conn = nil
		return err
	}
	return nil
}

func (k *KafkaBus) encodeProduceV2(topic string, partition int32, key, val []byte) []byte {
	// Inner Message v1: magic=1, attrs=0, timestamp, key, value
	var msgBody bytes.Buffer
	msgBody.WriteByte(1) // magic v1
	msgBody.WriteByte(0) // attributes: uncompressed
	_ = binary.Write(&msgBody, binary.BigEndian, time.Now().UnixMilli())
	_ = binary.Write(&msgBody, binary.BigEndian, int32(len(key)))
	msgBody.Write(key)
	_ = binary.Write(&msgBody, binary.BigEndian, int32(len(val)))
	msgBody.Write(val)

	crc := crc32.ChecksumIEEE(msgBody.Bytes())
	var msg bytes.Buffer
	_ = binary.Write(&msg, binary.BigEndian, crc)
	msg.Write(msgBody.Bytes())

	// MessageSet item: offset(8) + size(4) + msg
	var msgSet bytes.Buffer
	_ = binary.Write(&msgSet, binary.BigEndian, int64(0))
	_ = binary.Write(&msgSet, binary.BigEndian, int32(msg.Len()))
	msgSet.Write(msg.Bytes())

	// Produce Request v2: apiKey=0, apiVersion=2, corrID, clientID, acks=-1, timeout=3000
	var body bytes.Buffer
	_ = binary.Write(&body, binary.BigEndian, int16(0)) // Produce
	_ = binary.Write(&body, binary.BigEndian, int16(2)) // v2
	_ = binary.Write(&body, binary.BigEndian, k.corrID.Add(1))
	clientID := "cordova-server"
	_ = binary.Write(&body, binary.BigEndian, int16(len(clientID)))
	body.WriteString(clientID)
	_ = binary.Write(&body, binary.BigEndian, int16(-1))   // acks=all
	_ = binary.Write(&body, binary.BigEndian, int32(3000)) // timeout_ms
	_ = binary.Write(&body, binary.BigEndian, int32(1))    // topics count
	_ = binary.Write(&body, binary.BigEndian, int16(len(topic)))
	body.WriteString(topic)
	_ = binary.Write(&body, binary.BigEndian, int32(1)) // partitions count
	_ = binary.Write(&body, binary.BigEndian, partition)
	_ = binary.Write(&body, binary.BigEndian, int32(msgSet.Len()))
	body.Write(msgSet.Bytes())

	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, int32(body.Len()))
	frame.Write(body.Bytes())
	return frame.Bytes()
}

func (k *KafkaBus) Close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.conn != nil {
		_ = k.conn.Close()
		k.conn = nil
	}
}
