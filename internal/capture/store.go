package capture

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	StateInFlight    = "in_flight"
	StateComplete    = "complete"
	StateError       = "error"
	StateCanceled    = "canceled"
	StateInterrupted = "interrupted"
)

// Store keeps exchanges in memory and, when dir is non-empty, persists each
// finished exchange as one JSON file under dir/exchanges.
type Store struct {
	mu      sync.Mutex
	dir     string
	keep    int
	log     *slog.Logger
	order   []string
	records map[string]*Exchange
}

// New opens a store. An empty dir keeps records in memory only. Existing
// records are loaded and the oldest are pruned to keep.
func New(dir string, keep int, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Store{
		dir:     dir,
		keep:    keep,
		log:     logger,
		records: make(map[string]*Exchange),
	}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(s.exchangesDir(), 0o700); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Dir is the data root, or an empty string for a memory-only store.
func (s *Store) Dir() string { return s.dir }

// Count reports the number of retained exchanges.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.order)
}

// Begin creates an in-flight exchange and returns its ID.
func (s *Store) Begin(meta Meta) string {
	now := time.Now().UTC()
	id := newID(now)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[id] = &Exchange{
		ID:             id,
		StartedAt:      now,
		State:          StateInFlight,
		Method:         meta.Method,
		Path:           meta.Path,
		Query:          meta.Query,
		UpstreamURL:    meta.UpstreamURL,
		API:            meta.API,
		Status:         0,
		Stream:         meta.Stream,
		RequestHeaders: meta.RequestHeaders,
		RequestBody:    meta.RequestBody,
		Model:          meta.Model,
	}
	s.order = append(s.order, id)
	return id
}

// AppendEvent adds one streamed event. It never grows a record past MaxEvents.
func (s *Store) AppendEvent(id string, event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return
	}
	if len(record.Events) >= MaxEvents {
		record.EventsTruncated = true
		return
	}
	record.Events = append(record.Events, event)
}

// MarkEventsTruncated flags an exchange whose streamed events exceeded the cap
// before they reached AppendEvent.
func (s *Store) MarkEventsTruncated(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.records[id]; ok {
		record.EventsTruncated = true
	}
}

// Finish records the outcome and persists the exchange.
func (s *Store) Finish(id string, fin Finish) {
	s.mu.Lock()
	record, ok := s.records[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	state := fin.State
	if state == "" {
		state = StateComplete
	}
	record.State = state
	record.Status = fin.Status
	record.ResponseHeaders = fin.ResponseHeaders
	record.ResponseBody = fin.ResponseBody
	record.BodyTruncated = fin.BodyTruncated
	record.Stream = fin.Stream
	record.Usage = fin.Usage
	record.FinishReason = fin.FinishReason
	record.OutputText = fin.OutputText
	record.Error = fin.Error
	record.CompletedAt = time.Now().UTC()
	record.DurationMS = record.CompletedAt.Sub(record.StartedAt).Milliseconds()
	record.TTFBMS = fin.TTFB.Milliseconds()
	if fin.Model != "" {
		record.Model = fin.Model
	}

	snapshot := record.clone()
	path := s.pathFor(id)
	pruned := s.pruneLocked()
	s.mu.Unlock()

	if path != "" {
		if err := writeRecord(path, &snapshot); err != nil {
			s.log.Warn("persist exchange", "id", id, "error", err)
		}
	}
	for _, old := range pruned {
		if err := os.Remove(s.pathFor(old)); err != nil && !os.IsNotExist(err) {
			s.log.Warn("prune exchange", "id", old, "error", err)
		}
	}
}

// List returns summaries newest first.
func (s *Store) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Summary, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		record, ok := s.records[s.order[i]]
		if !ok {
			continue
		}
		summary := Summary{
			ID:           record.ID,
			StartedAt:    record.StartedAt,
			State:        record.State,
			Method:       record.Method,
			Path:         record.Path,
			API:          record.API,
			Model:        record.Model,
			Status:       record.Status,
			Stream:       record.Stream,
			DurationMS:   record.DurationMS,
			TTFBMS:       record.TTFBMS,
			FinishReason: record.FinishReason,
			OutputChars:  len(record.OutputText),
		}
		if record.Usage != nil {
			usage := *record.Usage
			summary.Usage = &usage
		}
		out = append(out, summary)
	}
	return out
}

// Get returns a snapshot of one exchange.
func (s *Store) Get(id string) (Exchange, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return Exchange{}, false
	}
	return record.clone(), true
}

// Clear removes every exchange from memory and disk.
func (s *Store) Clear() error {
	s.mu.Lock()
	s.records = make(map[string]*Exchange)
	s.order = nil
	dir := s.exchangesDir()
	persisted := s.dir != ""
	s.mu.Unlock()

	if !persisted {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Store) exchangesDir() string {
	if s.dir == "" {
		return ""
	}
	return filepath.Join(s.dir, "exchanges")
}

func (s *Store) pathFor(id string) string {
	if s.dir == "" {
		return ""
	}
	return filepath.Join(s.exchangesDir(), id+".json")
}

func (s *Store) load() error {
	entries, err := os.ReadDir(s.exchangesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	loaded := make([]*Exchange, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.exchangesDir(), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			s.log.Warn("read exchange", "path", path, "error", err)
			continue
		}
		var record Exchange
		if err := json.Unmarshal(data, &record); err != nil {
			s.log.Warn("parse exchange", "path", path, "error", err)
			continue
		}
		if record.ID == "" {
			record.ID = strings.TrimSuffix(entry.Name(), ".json")
		}
		if record.State == StateInFlight {
			record.State = StateInterrupted
		}
		loaded = append(loaded, &record)
	}
	sort.Slice(loaded, func(i, j int) bool {
		return loaded[i].StartedAt.Before(loaded[j].StartedAt)
	})
	for _, record := range loaded {
		s.records[record.ID] = record
		s.order = append(s.order, record.ID)
	}
	for _, id := range s.pruneLocked() {
		if err := os.Remove(s.pathFor(id)); err != nil && !os.IsNotExist(err) {
			s.log.Warn("prune exchange", "id", id, "error", err)
		}
	}
	return nil
}

// pruneLocked drops the oldest finished records beyond keep and returns their
// IDs. The caller must hold s.mu and removes the files after unlocking.
func (s *Store) pruneLocked() []string {
	if s.keep <= 0 {
		return nil
	}
	finished := 0
	for _, id := range s.order {
		if record, ok := s.records[id]; ok && record.State != StateInFlight {
			finished++
		}
	}
	remove := finished - s.keep
	if remove <= 0 {
		return nil
	}
	var pruned []string
	kept := s.order[:0]
	for _, id := range s.order {
		record, ok := s.records[id]
		if !ok {
			continue
		}
		if remove > 0 && record.State != StateInFlight {
			pruned = append(pruned, id)
			delete(s.records, id)
			remove--
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept
	return pruned
}

func writeRecord(path string, record *Exchange) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func newID(now time.Time) string {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return now.Format("20060102T150405.000000000Z")
	}
	return now.Format("20060102T150405.000Z") + "-" + hex.EncodeToString(suffix[:])
}
