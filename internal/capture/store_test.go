package capture

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T, dir string, keep int) *Store {
	t.Helper()
	store, err := New(dir, keep, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func beginTestExchange(store *Store, model string) string {
	return store.Begin(Meta{
		Method:      "POST",
		Path:        "/v1/chat/completions",
		API:         "chat_completions",
		Model:       model,
		RequestBody: FromBytes([]byte(`{"model":"` + model + `"}`)),
	})
}

func finishTestExchange(store *Store, id string, output string) {
	store.Finish(id, Finish{
		State:        StateComplete,
		Status:       http.StatusOK,
		ResponseBody: FromBytes([]byte(`{"ok":true}`)),
		Model:        "m",
		OutputText:   output,
		Usage:        &Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
		TTFB:         25 * time.Millisecond,
	})
}

func TestStoreLifecycle(t *testing.T) {
	store := testStore(t, "", 10)
	id := beginTestExchange(store, "deepseek-v4.1-flash")

	record, ok := store.Get(id)
	if !ok || record.State != StateInFlight {
		t.Fatalf("in-flight record = %#v, %v", record, ok)
	}

	store.AppendEvent(id, Event{Name: "response.output_text.delta", Data: map[string]any{"delta": "hi"}})
	finishTestExchange(store, id, "hi")

	record, ok = store.Get(id)
	if !ok || record.State != StateComplete {
		t.Fatalf("finished record = %#v, %v", record, ok)
	}
	if record.DurationMS < 0 || record.TTFBMS != 25 {
		t.Fatalf("timings = %dms / %dms", record.DurationMS, record.TTFBMS)
	}
	if record.OutputText != "hi" || record.Usage == nil || record.Usage.TotalTokens != 5 {
		t.Fatalf("record = %#v", record)
	}
	if len(record.Events) != 1 {
		t.Fatalf("events = %#v", record.Events)
	}

	list := store.List()
	if len(list) != 1 || list[0].ID != id || list[0].Model != "m" {
		t.Fatalf("list = %#v", list)
	}
}

func TestStoreBoundsEvents(t *testing.T) {
	store := testStore(t, "", 10)
	id := beginTestExchange(store, "m")
	for i := 0; i < MaxEvents+5; i++ {
		store.AppendEvent(id, Event{Data: i})
	}
	record, _ := store.Get(id)
	if len(record.Events) != MaxEvents || !record.EventsTruncated {
		t.Fatalf("events = %d, truncated = %v", len(record.Events), record.EventsTruncated)
	}
}

func TestStorePersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	store := testStore(t, dir, 10)
	id := beginTestExchange(store, "m")
	finishTestExchange(store, id, "hello")

	path := filepath.Join(dir, "exchanges", id+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v", info.Mode().Perm())
	}

	reloaded := testStore(t, dir, 10)
	record, ok := reloaded.Get(id)
	if !ok || record.State != StateComplete || record.OutputText != "hello" {
		t.Fatalf("reloaded = %#v, %v", record, ok)
	}
}

func TestStoreMarksAbandonedInFlightRecordInterrupted(t *testing.T) {
	dir := t.TempDir()
	store := testStore(t, dir, 10)
	id := beginTestExchange(store, "m")
	// Simulate a crash by writing the in-flight record the way Finish would.
	record, _ := store.Get(id)
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, "exchanges", id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	reloaded := testStore(t, dir, 10)
	record, ok := reloaded.Get(id)
	if !ok || record.State != StateInterrupted {
		t.Fatalf("reloaded = %#v, %v", record, ok)
	}
}

func TestStoreRetention(t *testing.T) {
	dir := t.TempDir()
	store := testStore(t, dir, 2)
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		id := beginTestExchange(store, "m")
		finishTestExchange(store, id, "x")
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond)
	}
	if got := store.Count(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	if _, ok := store.Get(ids[0]); ok {
		t.Fatal("oldest exchange was not pruned")
	}
	for _, id := range ids[1:] {
		if _, err := os.Stat(filepath.Join(dir, "exchanges", id+".json")); err != nil {
			t.Fatalf("kept exchange file missing: %v", err)
		}
	}
}

func TestStoreClear(t *testing.T) {
	dir := t.TempDir()
	store := testStore(t, dir, 10)
	id := beginTestExchange(store, "m")
	finishTestExchange(store, id, "x")

	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if store.Count() != 0 {
		t.Fatalf("count = %d", store.Count())
	}
	entries, err := os.ReadDir(filepath.Join(dir, "exchanges"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("files remain: %v", entries)
	}
}

func TestRedactHeaders(t *testing.T) {
	headers := http.Header{
		"Authorization": {"Bearer secret"},
		"X-Api-Key":     {"secret"},
		"Content-Type":  {"application/json"},
		"Cookie":        {"session=1"},
	}
	redacted := RedactHeaders(headers)
	if redacted["Authorization"][0] != "<redacted>" {
		t.Fatalf("authorization = %v", redacted["Authorization"])
	}
	if redacted["X-Api-Key"][0] != "<redacted>" {
		t.Fatalf("x-api-key = %v", redacted["X-Api-Key"])
	}
	if redacted["Cookie"][0] != "<redacted>" {
		t.Fatalf("cookie = %v", redacted["Cookie"])
	}
	if redacted["Content-Type"][0] != "application/json" {
		t.Fatalf("content-type = %v", redacted["Content-Type"])
	}
}
