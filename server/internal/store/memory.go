package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type MemoryStore struct {
	mu       sync.RWMutex
	data     *Tx
	snapPath string
	clock    func() time.Time
}

func newEmptyTx() *Tx {
	return &Tx{
		Accounts:       make(map[string]*Account),
		UsernameIndex:  make(map[string]string),
		Sessions:       make(map[string]*Session),
		StepUpTokens:   make(map[string]*StepUpToken),
		APIKeys:        make(map[string]*APIKeyRecord),
		KeyHashIndex:   make(map[string]string),
		Enrollments:    make(map[string]*Enrollment),
		UserCodeIndex:  make(map[string]string),
		DeviceCodeIdx:  make(map[string]string),
		Nodes:          make(map[string]*NodeRecord),
		NodeChallenges: make(map[string]*NodeChallenge),
		UsedJTIs:       make(map[string]time.Time),
		Tombstones:     make(map[string]*OperationTombstone),
		Idempotency:    make(map[string]*IdempotencyRecord),
		LiveEvents:     make(map[string][]LiveEvent),
		ResourceRev:    make(map[string]int64),
	}
}

func NewMemoryStore(snapPath string, clock func() time.Time) (*MemoryStore, error) {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	s := &MemoryStore{
		data:     newEmptyTx(),
		snapPath: snapPath,
		clock:    clock,
	}
	if snapPath != "" {
		if raw, err := os.ReadFile(snapPath); err == nil {
			var loaded Tx
			if err := json.Unmarshal(raw, &loaded); err != nil {
				return nil, err
			}
			s.ensureMaps(&loaded)
			s.data = &loaded
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return s, nil
}

func (s *MemoryStore) ensureMaps(t *Tx) {
	if t.Accounts == nil { t.Accounts = make(map[string]*Account) }
	if t.UsernameIndex == nil { t.UsernameIndex = make(map[string]string) }
	if t.Sessions == nil { t.Sessions = make(map[string]*Session) }
	if t.StepUpTokens == nil { t.StepUpTokens = make(map[string]*StepUpToken) }
	if t.APIKeys == nil { t.APIKeys = make(map[string]*APIKeyRecord) }
	if t.KeyHashIndex == nil { t.KeyHashIndex = make(map[string]string) }
	if t.Enrollments == nil { t.Enrollments = make(map[string]*Enrollment) }
	if t.UserCodeIndex == nil { t.UserCodeIndex = make(map[string]string) }
	if t.DeviceCodeIdx == nil { t.DeviceCodeIdx = make(map[string]string) }
	if t.Nodes == nil { t.Nodes = make(map[string]*NodeRecord) }
	if t.NodeChallenges == nil { t.NodeChallenges = make(map[string]*NodeChallenge) }
	if t.UsedJTIs == nil { t.UsedJTIs = make(map[string]time.Time) }
	if t.Tombstones == nil { t.Tombstones = make(map[string]*OperationTombstone) }
	if t.Idempotency == nil { t.Idempotency = make(map[string]*IdempotencyRecord) }
	if t.LiveEvents == nil { t.LiveEvents = make(map[string][]LiveEvent) }
	if t.ResourceRev == nil { t.ResourceRev = make(map[string]int64) }
}

func (s *MemoryStore) Now() time.Time {
	return s.clock()
}

func (s *MemoryStore) WithLock(fn func(tx *Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(s.data); err != nil {
		return err
	}
	if s.snapPath != "" {
		return s.persistLocked()
	}
	return nil
}

func (s *MemoryStore) WithReadLock(fn func(tx *Tx) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fn(s.data)
}

func (s *MemoryStore) persistLocked() error {
	dir := filepath.Dir(s.snapPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.snapPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.snapPath)
}

// EmitEventLocked appends a C4 dashboard invalidation event inside WithLock.
func EmitEventLocked(tx *Tx, accountID, topic, resourceID string) {
	tx.LiveSeq++
	revKey := accountID + ":" + topic + ":" + resourceID
	tx.ResourceRev[revKey]++
	ev := LiveEvent{
		EventID:    time.Now().UTC().Format("20060102150405") + "-" + string(rune('0'+tx.LiveSeq%10)),
		AccountID:  accountID,
		Topic:      topic,
		ResourceID: resourceID,
		Revision:   tx.ResourceRev[revKey],
		Action:     "invalidate",
	}
	ev.EventID = formatEventID(tx.LiveSeq)
	list := append(tx.LiveEvents[accountID], ev)
	if len(list) > 10000 {
		list = list[len(list)-10000:]
	}
	tx.LiveEvents[accountID] = list
}

func formatEventID(seq int64) string {
	// simple monotonic decimal string
	if seq == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for seq > 0 {
		i--
		buf[i] = byte('0' + seq%10)
		seq /= 10
	}
	return string(buf[i:])
}
