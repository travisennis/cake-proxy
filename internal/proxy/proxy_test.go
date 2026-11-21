package proxy

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/travisennis/cake-proxy/internal/capture"
)

func setup(t *testing.T, upstream http.Handler, mutate func(*Options)) (*capture.Store, *httptest.Server) {
	t.Helper()
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)

	store, err := capture.New("", 50, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(upstreamServer.URL + "/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	options := Options{
		Upstream:     target,
		Store:        store,
		MaxBodyBytes: 1 << 20,
		Logger:       slog.New(slog.DiscardHandler),
	}
	if mutate != nil {
		mutate(&options)
	}
	relay, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	proxyServer := httptest.NewServer(relay)
	t.Cleanup(proxyServer.Close)
	return store, proxyServer
}

func waitForRecord(t *testing.T, store *capture.Store, id string) capture.Exchange {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		record, ok := store.Get(id)
		if ok && record.State != capture.StateInFlight {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("exchange %s did not finish; last state %q", id, record.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type seenRequest struct {
	path string
	auth string
	body string
}

func TestChatCompletionRelayAndCapture(t *testing.T) {
	seen := make(chan seenRequest, 1)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- seenRequest{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(body)}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"chatcmpl-1",
			"model":"deepseek-v4.1-flash",
			"choices":[{"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}
		}`)
	})
	store, server := setup(t, upstream, func(options *Options) {
		options.UpstreamKey = "upstream-secret"
	})

	requestBody := `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer placeholder")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(response.Body)

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, responseBody)
	}
	got := <-seen
	if got.path != "/api/v1/chat/completions" {
		t.Fatalf("upstream path = %q", got.path)
	}
	if got.auth != "Bearer upstream-secret" {
		t.Fatalf("upstream authorization = %q", got.auth)
	}
	if got.body != requestBody {
		t.Fatalf("upstream body = %q", got.body)
	}
	if !strings.Contains(string(responseBody), `"Hello"`) {
		t.Fatalf("response body = %s", responseBody)
	}
	id := response.Header.Get("X-Cake-Proxy-Exchange")
	if id == "" {
		t.Fatal("missing exchange id header")
	}

	record := waitForRecord(t, store, id)
	if record.State != capture.StateComplete || record.Status != http.StatusOK {
		t.Fatalf("record = %#v", record)
	}
	if record.API != "chat_completions" || record.Model != "deepseek-v4.1-flash" {
		t.Fatalf("record api/model = %q / %q", record.API, record.Model)
	}
	if record.OutputText != "Hello" || record.FinishReason != "stop" {
		t.Fatalf("output = %q, finish = %q", record.OutputText, record.FinishReason)
	}
	if record.Usage == nil || record.Usage.TotalTokens != 13 {
		t.Fatalf("usage = %#v", record.Usage)
	}
	if record.RequestHeaders["Authorization"][0] != "<redacted>" {
		t.Fatalf("stored authorization = %v", record.RequestHeaders["Authorization"])
	}
	if len(record.RequestBody.JSON) == 0 {
		t.Fatalf("request body not captured: %#v", record.RequestBody)
	}
	if record.Stream {
		t.Fatal("non-streaming response marked as stream")
	}
}

func TestStreamingResponsesRelayAndCapture(t *testing.T) {
	release := make(chan struct{})
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"delta\":\"Hel\"}\n\n")
		flusher.Flush()
		<-release
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"delta\":\"lo\"}\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: {\"response\":{\"model\":\"gpt-x\",\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":2,\"total_tokens\":9}}}\n\n")
		flusher.Flush()
	})
	store, server := setup(t, upstream, nil)

	request, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/v1/responses",
		strings.NewReader(`{"model":"gpt-x","stream":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	reader := bufio.NewReader(response.Body)
	type readResult struct {
		line string
		err  error
	}
	first := make(chan readResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		first <- readResult{line: line, err: err}
	}()

	select {
	case result := <-first:
		if result.err != nil {
			close(release)
			t.Fatalf("read first event: %v", result.err)
		}
		if !strings.Contains(result.line, "response.output_text.delta") {
			close(release)
			t.Fatalf("first line = %q", result.line)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("proxy buffered the first SSE event instead of flushing it")
	}
	close(release)

	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), `"lo"`) {
		t.Fatalf("remaining stream = %s", rest)
	}

	id := response.Header.Get("X-Cake-Proxy-Exchange")
	record := waitForRecord(t, store, id)
	if !record.Stream || record.State != capture.StateComplete {
		t.Fatalf("record = %#v", record)
	}
	if len(record.Events) != 3 {
		t.Fatalf("events = %d: %#v", len(record.Events), record.Events)
	}
	if record.OutputText != "Hello" || record.FinishReason != "completed" {
		t.Fatalf("output = %q, finish = %q", record.OutputText, record.FinishReason)
	}
	if record.Usage == nil || record.Usage.InputTokens != 7 || record.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %#v", record.Usage)
	}
}

func TestUpstreamErrorPassesThrough(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
	})
	store, server := setup(t, upstream, nil)

	response, err := (&http.Client{Timeout: 5 * time.Second}).Post(
		server.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"m"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(body), "slow down") {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}

	record := waitForRecord(t, store, response.Header.Get("X-Cake-Proxy-Exchange"))
	if record.Status != http.StatusTooManyRequests || record.State != capture.StateComplete {
		t.Fatalf("record = %#v", record)
	}
	if string(record.ResponseBody.JSON) == "" {
		t.Fatalf("error body not captured: %#v", record.ResponseBody)
	}
}

func TestRequestTooLarge(t *testing.T) {
	var called atomic.Bool
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
	})
	store, server := setup(t, upstream, func(options *Options) {
		options.MaxBodyBytes = 64
	})

	response, err := (&http.Client{Timeout: 5 * time.Second}).Post(
		server.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(strings.Repeat("x", 200)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if called.Load() {
		t.Fatal("oversized request reached the upstream")
	}
	if store.Count() != 0 {
		t.Fatalf("oversized request was recorded: %d", store.Count())
	}
}

func TestMislabeledSSEBodyIsParsed(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	store, server := setup(t, upstream, nil)

	response, err := (&http.Client{Timeout: 5 * time.Second}).Post(
		server.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"m"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.ReadAll(response.Body)

	record := waitForRecord(t, store, response.Header.Get("X-Cake-Proxy-Exchange"))
	if !record.Stream || len(record.Events) != 2 {
		t.Fatalf("stream = %v, events = %#v", record.Stream, record.Events)
	}
	if record.OutputText != "Hi" {
		t.Fatalf("output = %q", record.OutputText)
	}
}

func TestCustomCredentialHeader(t *testing.T) {
	seen := make(chan [2]string, 1)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- [2]string{r.Header.Get("X-Api-Key"), r.Header.Get("Authorization")}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	})
	_, server := setup(t, upstream, func(options *Options) {
		options.UpstreamKey = "k"
		options.UpstreamKeyHeader = "X-Api-Key"
	})

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"m"}`))
	request.Header.Set("Authorization", "Bearer placeholder")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	got := <-seen
	if got[0] != "k" {
		t.Fatalf("upstream x-api-key = %q", got[0])
	}
	if got[1] != "" {
		t.Fatalf("client authorization leaked: %q", got[1])
	}
}

func TestUpstreamFailureIsRecorded(t *testing.T) {
	store, err := capture.New("", 10, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	relay, err := New(Options{
		Upstream:     &url.URL{Scheme: "http", Host: "127.0.0.1:1"},
		Store:        store,
		MaxBodyBytes: 1 << 20,
		Logger:       slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay)
	defer server.Close()

	response, err := (&http.Client{Timeout: 5 * time.Second}).Post(
		server.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"m"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", response.StatusCode)
	}

	id := response.Header.Get("X-Cake-Proxy-Exchange")
	record := waitForRecord(t, store, id)
	if record.State != capture.StateError || record.Error == "" {
		t.Fatalf("record = %#v", record)
	}
}

func TestRedactQuery(t *testing.T) {
	cases := map[string]string{
		"":                               "",
		"foo=bar":                        "foo=bar",
		"api_key=secret&foo=bar":         "api_key=redacted&foo=bar",
		"token=a&key=b":                  "key=redacted&token=redacted",
		"access_token=x&authorization=y": "access_token=redacted&authorization=redacted",
	}
	for input, want := range cases {
		if got := redactQuery(input); got != want {
			t.Errorf("redactQuery(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var followed atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Store(true)
	}))
	defer elsewhere.Close()

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", elsewhere.URL+"/elsewhere")
		w.WriteHeader(http.StatusTemporaryRedirect)
		fmt.Fprint(w, "redirect body")
	})
	store, server := setup(t, upstream, nil)

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Post(
		server.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"m"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusTemporaryRedirect || string(body) != "redirect body" {
		t.Fatalf("status = %d, body = %q", response.StatusCode, body)
	}
	if followed.Load() {
		t.Fatal("proxy followed the upstream redirect")
	}
	record := waitForRecord(t, store, response.Header.Get("X-Cake-Proxy-Exchange"))
	if record.Status != http.StatusTemporaryRedirect {
		t.Fatalf("record status = %d", record.Status)
	}
}

func TestUnknownPathIsRejected(t *testing.T) {
	_, server := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called")
	}), nil)

	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(server.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
}
