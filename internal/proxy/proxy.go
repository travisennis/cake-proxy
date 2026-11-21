// Package proxy relays OpenAI-compatible requests to one upstream and records
// each exchange. Bodies, statuses, headers, and streaming bytes pass through
// unchanged; the only deliberate rewrite is the upstream credential.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/travisennis/cake-proxy/internal/capture"
	"github.com/travisennis/cake-proxy/internal/openai"
	"github.com/travisennis/cake-proxy/internal/sse"
)

// Options configures the relay.
type Options struct {
	Upstream          *url.URL
	UpstreamKey       string
	UpstreamKeyHeader string
	Store             *capture.Store
	MaxBodyBytes      int64
	Logger            *slog.Logger
}

// Proxy is an http.Handler that forwards /v1 requests and captures them.
type Proxy struct {
	upstream  *url.URL
	key       string
	keyHeader string
	store     *capture.Store
	client    *http.Client
	maxBody   int64
	log       *slog.Logger
	openCode  bool
	warnOnce  sync.Once
}

// New validates opts and builds the relay.
func New(opts Options) (*Proxy, error) {
	if opts.Upstream == nil {
		return nil, errors.New("upstream URL is required")
	}
	upstream := *opts.Upstream
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return nil, fmt.Errorf("upstream must be http or https, got %q", upstream.Scheme)
	}
	if upstream.Host == "" {
		return nil, errors.New("upstream URL has no host")
	}
	if upstream.User != nil {
		return nil, errors.New("upstream URL must not embed credentials; use -upstream-key-env")
	}
	if upstream.RawQuery != "" || upstream.Fragment != "" {
		return nil, errors.New("upstream URL must not include a query or fragment")
	}
	upstream.Path = strings.TrimRight(upstream.Path, "/")
	upstream.RawPath = ""

	keyHeader := opts.UpstreamKeyHeader
	if keyHeader == "" {
		keyHeader = "Authorization"
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 64 << 20
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
	}

	return &Proxy{
		upstream:  &upstream,
		key:       opts.UpstreamKey,
		keyHeader: keyHeader,
		store:     opts.Store,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxBody:  opts.MaxBodyBytes,
		log:      logger,
		openCode: isOpenCodeHost(upstream.Hostname()),
	}, nil
}

// Upstream reports the configured upstream base URL.
func (p *Proxy) Upstream() string {
	return p.upstream.String()
}

// ServeHTTP relays one request. The caller only routes API paths here.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	suffix := upstreamSuffix(r.URL.Path)
	if suffix == "" {
		http.Error(w, "cake-proxy: not an API path", http.StatusNotFound)
		return
	}

	body, err := readRequestBody(w, r, p.maxBody)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, fmt.Sprintf("cake-proxy: request body exceeds %d bytes", p.maxBody), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "cake-proxy: read request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	target := *p.upstream
	target.Path = target.Path + suffix
	target.RawQuery = r.URL.RawQuery

	api := apiForPath(suffix)
	model, requestedStream := openai.ProbeRequest(body)
	recordQuery := redactQuery(r.URL.RawQuery)
	recordTarget := target
	recordTarget.RawQuery = recordQuery

	exchangeID := p.store.Begin(capture.Meta{
		Method:         r.Method,
		Path:           r.URL.Path,
		Query:          recordQuery,
		UpstreamURL:    recordTarget.String(),
		API:            api,
		Model:          model,
		Stream:         requestedStream,
		RequestHeaders: capture.RedactHeaders(r.Header),
		RequestBody:    capture.FromBytes(body),
	})
	w.Header().Set("X-Cake-Proxy-Exchange", exchangeID)

	upstreamRequest, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		p.finishError(exchangeID, http.StatusInternalServerError, "build upstream request: "+err.Error())
		http.Error(w, "cake-proxy: build upstream request", http.StatusInternalServerError)
		return
	}
	copyRequestHeaders(upstreamRequest.Header, r.Header)
	if p.key != "" {
		upstreamRequest.Header.Del("Authorization")
		upstreamRequest.Header.Del(p.keyHeader)
		if strings.EqualFold(p.keyHeader, "Authorization") {
			upstreamRequest.Header.Set(p.keyHeader, "Bearer "+p.key)
		} else {
			upstreamRequest.Header.Set(p.keyHeader, p.key)
		}
	}
	if p.openCode && upstreamRequest.Header.Get("x-opencode-session") == "" {
		p.warnOnce.Do(func() {
			p.log.Warn("OpenCode upstream request has no x-opencode-session; set provider = \"opencode\" on the cake model entry", "path", r.URL.Path)
		})
	}

	start := time.Now()
	response, err := p.client.Do(upstreamRequest)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.Canceled) {
			status = 499
		}
		p.finishError(exchangeID, status, "upstream request failed: "+err.Error())
		http.Error(w, "cake-proxy: upstream request failed: "+err.Error(), status)
		return
	}
	defer response.Body.Close()

	contentType := response.Header.Get("Content-Type")
	isSSE := strings.HasPrefix(strings.ToLower(contentType), "text/event-stream")
	copyResponseHeaders(w.Header(), response.Header, isSSE)
	w.Header().Set("X-Cake-Proxy-Exchange", exchangeID)
	w.WriteHeader(response.StatusCode)

	timed := &firstByteReader{reader: response.Body, start: start}
	relay := p.relay(w, timed, isSSE, exchangeID, r.Context())

	var responseBody capture.Body
	capturedEvents := relay.events
	if !isSSE {
		if looksLikeSSE(relay.captured) {
			isSSE = true
			var eventsTruncated bool
			capturedEvents, eventsTruncated = parseEvents(relay.captured)
			for _, event := range capturedEvents {
				p.store.AppendEvent(exchangeID, event)
			}
			if eventsTruncated {
				p.store.MarkEventsTruncated(exchangeID)
			}
		} else {
			responseBody = capture.FromBytes(relay.captured)
		}
	}

	analysis := openai.Analyze(api, responseBody, capturedEvents)
	if analysis.Model == "" {
		analysis.Model = model
	}

	state := capture.StateComplete
	recordError := ""
	switch {
	case relay.writeErr != nil || r.Context().Err() != nil:
		state = capture.StateCanceled
		if relay.writeErr != nil {
			recordError = "client write failed: " + relay.writeErr.Error()
		} else {
			recordError = "client canceled the request"
		}
	case relay.readErr != nil && !errors.Is(relay.readErr, io.EOF):
		state = capture.StateError
		recordError = "read upstream response: " + relay.readErr.Error()
	}

	p.store.Finish(exchangeID, capture.Finish{
		State:           state,
		Status:          response.StatusCode,
		ResponseHeaders: capture.RedactHeaders(response.Header),
		ResponseBody:    responseBody,
		BodyTruncated:   relay.truncated,
		Model:           analysis.Model,
		FinishReason:    analysis.FinishReason,
		OutputText:      analysis.OutputText,
		Usage:           analysis.Usage,
		Stream:          isSSE,
		TTFB:            timed.first,
		Error:           recordError,
	})
}

func (p *Proxy) finishError(id string, status int, message string) {
	p.store.Finish(id, capture.Finish{
		State:  capture.StateError,
		Status: status,
		Error:  message,
	})
}

type relayResult struct {
	captured  []byte
	truncated bool
	events    []capture.Event
	writeErr  error
	readErr   error
}

// relay copies provider bytes to the client, flushing each chunk for SSE, and
// keeps a bounded copy for inspection. It must never buffer a stream.
func (p *Proxy) relay(w http.ResponseWriter, reader io.Reader, isSSE bool, id string, ctx context.Context) relayResult {
	flusher, canFlush := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	var captured bytes.Buffer
	var parser sse.Parser
	var result relayResult

	emit := func(event sse.Event) {
		if len(result.events) >= capture.MaxEvents {
			return
		}
		converted := convertEvent(event)
		result.events = append(result.events, converted)
		p.store.AppendEvent(id, converted)
	}

	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			if !result.truncated {
				remaining := p.maxBody - int64(captured.Len())
				if remaining <= 0 {
					result.truncated = true
				} else {
					take := int64(n)
					if take > remaining {
						take = remaining
						result.truncated = true
					}
					captured.Write(buffer[:take])
				}
			}
			if isSSE {
				parser.Feed(buffer[:n], emit)
			}
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				result.writeErr = writeErr
				result.readErr = writeErr
				break
			}
			if isSSE && canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			result.readErr = err
			break
		}
		if ctx.Err() != nil {
			result.readErr = ctx.Err()
			break
		}
	}
	if isSSE {
		parser.Flush(emit)
	}
	result.captured = captured.Bytes()
	return result
}

func readRequestBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	return io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
}

// upstreamSuffix strips the proxy's /v1 mount so that
// http://proxy/v1/chat/completions maps to <upstream>/chat/completions.
func upstreamSuffix(path string) string {
	if path == "/v1" || path == "/v1/" {
		return "/"
	}
	if strings.HasPrefix(path, "/v1/") {
		return path[len("/v1"):]
	}
	if path == "/chat/completions" || path == "/responses" {
		return path
	}
	return ""
}

func apiForPath(suffix string) string {
	switch {
	case strings.HasSuffix(suffix, "/chat/completions"):
		return openai.APIChatCompletions
	case strings.HasSuffix(suffix, "/responses"):
		return openai.APIResponses
	default:
		return openai.APIOther
	}
}

func convertEvent(event sse.Event) capture.Event {
	var data any
	if err := json.Unmarshal([]byte(event.Data), &data); err != nil {
		data = event.Data
	}
	return capture.Event{Name: event.Name, Data: data}
}

func parseEvents(raw []byte) ([]capture.Event, bool) {
	var parser sse.Parser
	var events []capture.Event
	truncated := false
	emit := func(event sse.Event) {
		if len(events) < capture.MaxEvents {
			events = append(events, convertEvent(event))
			return
		}
		truncated = true
	}
	parser.Feed(raw, emit)
	parser.Flush(emit)
	return events, truncated
}

func redactQuery(raw string) string {
	if raw == "" {
		return ""
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return raw
	}
	changed := false
	for key := range values {
		switch strings.ToLower(key) {
		case "api_key", "apikey", "key", "token", "access_token", "authorization":
			values[key] = []string{"redacted"}
			changed = true
		}
	}
	if !changed {
		return raw
	}
	return values.Encode()
}

func looksLikeSSE(raw []byte) bool {
	trimmed := bytes.TrimLeft(raw, "\xef\xbb\xbf \t\r\n")
	return bytes.HasPrefix(trimmed, []byte("data:")) ||
		bytes.HasPrefix(trimmed, []byte("event:"))
}

func isOpenCodeHost(host string) bool {
	host = strings.ToLower(host)
	return host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai")
}

func copyRequestHeaders(dst, src http.Header) {
	drop := hopByHop(src)
	for name, values := range src {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if drop[canonical] {
			continue
		}
		switch canonical {
		case "Host", "Content-Length", "Accept-Encoding":
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}

func copyResponseHeaders(dst, src http.Header, streaming bool) {
	drop := hopByHop(src)
	for name, values := range src {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if drop[canonical] {
			continue
		}
		if streaming && canonical == "Content-Length" {
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}

func hopByHop(header http.Header) map[string]bool {
	drop := map[string]bool{
		"Connection":          true,
		"Proxy-Connection":    true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailer":             true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
	}
	for _, value := range header["Connection"] {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if token != "" {
				drop[textproto.CanonicalMIMEHeaderKey(token)] = true
			}
		}
	}
	return drop
}

// firstByteReader records when the provider's first response byte arrives.
type firstByteReader struct {
	reader io.Reader
	start  time.Time
	first  time.Duration
	seen   bool
}

func (f *firstByteReader) Read(p []byte) (int, error) {
	n, err := f.reader.Read(p)
	if n > 0 && !f.seen {
		f.seen = true
		f.first = time.Since(f.start)
	}
	return n, err
}
