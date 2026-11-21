package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/travisennis/cake-proxy/internal/capture"
)

func testServer(t *testing.T) (*capture.Store, *httptest.Server) {
	t.Helper()
	store, err := capture.New("", 10, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(store, Info{
		Upstream: "https://example.test/v1",
		Listen:   "127.0.0.1:8787",
		Keep:     10,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return store, server
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response, string(body)
}

func TestIndexAndAssets(t *testing.T) {
	_, server := testServer(t)

	response, body := get(t, server.URL+"/")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if !strings.Contains(body, "cake-proxy") || !strings.Contains(body, "/assets/app.js") {
		t.Fatalf("index body = %s", body)
	}

	response, body = get(t, server.URL+"/assets/app.js")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("asset status = %d", response.StatusCode)
	}
	if !strings.Contains(body, "function poll") {
		t.Fatalf("asset body does not look like the app: %s", body[:min(len(body), 200)])
	}
}

func TestConfig(t *testing.T) {
	_, server := testServer(t)
	response, body := get(t, server.URL+"/api/config")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(body), &config); err != nil {
		t.Fatal(err)
	}
	if config["upstream"] != "https://example.test/v1" || config["listen"] != "127.0.0.1:8787" {
		t.Fatalf("config = %#v", config)
	}
}

func TestExchangeListAndDetail(t *testing.T) {
	store, server := testServer(t)
	id := store.Begin(capture.Meta{
		Method: "POST",
		Path:   "/v1/chat/completions",
		API:    "chat_completions",
		Model:  "m",
	})
	store.AppendEvent(id, capture.Event{Data: map[string]any{"delta": "hi"}})
	store.Finish(id, capture.Finish{
		State:      capture.StateComplete,
		Status:     http.StatusOK,
		OutputText: "hi",
		Usage:      &capture.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})

	response, body := get(t, server.URL+"/api/exchanges")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var list struct {
		Exchanges []capture.Summary `json:"exchanges"`
		Count     int               `json:"count"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || list.Exchanges[0].ID != id || list.Exchanges[0].Usage.TotalTokens != 2 {
		t.Fatalf("list = %#v", list)
	}

	response, body = get(t, server.URL+"/api/exchanges/"+id)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("detail status = %d", response.StatusCode)
	}
	var exchange capture.Exchange
	if err := json.Unmarshal([]byte(body), &exchange); err != nil {
		t.Fatal(err)
	}
	if exchange.ID != id || exchange.OutputText != "hi" || len(exchange.Events) != 1 {
		t.Fatalf("exchange = %#v", exchange)
	}
}

func TestMissingExchange(t *testing.T) {
	_, server := testServer(t)
	response, body := get(t, server.URL+"/api/exchanges/nope")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
}

func TestClear(t *testing.T) {
	store, server := testServer(t)
	id := store.Begin(capture.Meta{Method: "POST", Path: "/v1/responses", API: "responses"})
	store.Finish(id, capture.Finish{State: capture.StateComplete, Status: 200})

	request, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/exchanges", nil)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if store.Count() != 0 {
		t.Fatalf("count = %d", store.Count())
	}
}

func TestUnknownPath(t *testing.T) {
	_, server := testServer(t)
	response, body := get(t, server.URL+"/unknown")
	if response.StatusCode != http.StatusNotFound || !strings.Contains(body, "/v1") {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
}
