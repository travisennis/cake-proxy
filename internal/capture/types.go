// Package capture stores the request/response exchanges that pass through the
// proxy. Records live in memory for the live UI and are written to disk when
// the response finishes.
package capture

import (
	"encoding/json"
	"net/http"
	"time"
)

// MaxEvents is the number of streamed events kept per exchange. Providers emit
// one event per output token, so a runaway stream must be bounded.
const MaxEvents = 20000

// Usage is the provider-reported token usage, normalized across the Chat
// Completions and Responses APIs.
type Usage struct {
	InputTokens       int64 `json:"input_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens,omitempty"`
	TotalTokens       int64 `json:"total_tokens,omitempty"`
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty"`
	ReasoningTokens   int64 `json:"reasoning_tokens,omitempty"`
}

// Body is a captured payload. Valid JSON is kept as raw JSON so the UI can
// render its structure; anything else is kept as text.
type Body struct {
	JSON json.RawMessage `json:"json,omitempty"`
	Text string          `json:"text,omitempty"`
}

// FromBytes builds a Body and takes ownership of raw.
func FromBytes(raw []byte) Body {
	if len(raw) == 0 {
		return Body{}
	}
	if json.Valid(raw) {
		return Body{JSON: json.RawMessage(raw)}
	}
	return Body{Text: string(raw)}
}

// IsZero reports whether nothing was captured.
func (b Body) IsZero() bool { return len(b.JSON) == 0 && b.Text == "" }

// Event is one streamed server-sent event. Data is the parsed JSON value when
// the event payload is JSON, otherwise the raw string.
type Event struct {
	Name string `json:"name,omitempty"`
	Data any    `json:"data,omitempty"`
}

// Exchange is one request/response pair. Fields are filled in as the exchange
// progresses so the UI can watch a live stream.
type Exchange struct {
	ID          string    `json:"id"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at,omitzero"`
	State       string    `json:"state"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Query       string    `json:"query,omitempty"`
	UpstreamURL string    `json:"upstream_url"`
	API         string    `json:"api"`
	Status      int       `json:"status,omitempty"`
	Stream      bool      `json:"stream"`

	RequestHeaders  map[string][]string `json:"request_headers,omitempty"`
	RequestBody     Body                `json:"request_body,omitzero"`
	ResponseHeaders map[string][]string `json:"response_headers,omitempty"`
	ResponseBody    Body                `json:"response_body,omitzero"`
	Events          []Event             `json:"events,omitempty"`
	EventsTruncated bool                `json:"events_truncated,omitempty"`
	BodyTruncated   bool                `json:"body_truncated,omitempty"`

	Model        string `json:"model,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
	OutputText   string `json:"output_text,omitempty"`
	Error        string `json:"error,omitempty"`

	Usage      *Usage `json:"usage,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	TTFBMS     int64  `json:"ttfb_ms,omitempty"`
}

// Summary is the list-view projection of an exchange.
type Summary struct {
	ID           string    `json:"id"`
	StartedAt    time.Time `json:"started_at"`
	State        string    `json:"state"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	API          string    `json:"api"`
	Model        string    `json:"model,omitempty"`
	Status       int       `json:"status,omitempty"`
	Stream       bool      `json:"stream"`
	DurationMS   int64     `json:"duration_ms,omitempty"`
	TTFBMS       int64     `json:"ttfb_ms,omitempty"`
	Usage        *Usage    `json:"usage,omitempty"`
	FinishReason string    `json:"finish_reason,omitempty"`
	OutputChars  int       `json:"output_chars,omitempty"`
}

// Meta is the information known before the upstream request is sent.
type Meta struct {
	Method         string
	Path           string
	Query          string
	UpstreamURL    string
	API            string
	Model          string
	Stream         bool
	RequestHeaders map[string][]string
	RequestBody    Body
}

// Finish is the information known when the exchange ends.
type Finish struct {
	State           string
	Status          int
	ResponseHeaders map[string][]string
	ResponseBody    Body
	BodyTruncated   bool
	Model           string
	FinishReason    string
	OutputText      string
	Usage           *Usage
	Stream          bool
	TTFB            time.Duration
	Error           string
}

// clone returns a copy that can be marshaled without racing the relay loop.
func (e *Exchange) clone() Exchange {
	out := *e
	out.RequestHeaders = cloneHeader(e.RequestHeaders)
	out.ResponseHeaders = cloneHeader(e.ResponseHeaders)
	if e.Events != nil {
		out.Events = append([]Event(nil), e.Events...)
	}
	if e.Usage != nil {
		usage := *e.Usage
		out.Usage = &usage
	}
	return out
}

func cloneHeader(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, vv := range in {
		out[k] = append([]string(nil), vv...)
	}
	return out
}

// RedactHeaders copies headers for storage and removes credential values.
func RedactHeaders(h http.Header) map[string][]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string][]string, len(h))
	for k, vv := range h {
		if isCredentialHeader(k) {
			out[k] = []string{"<redacted>"}
			continue
		}
		out[k] = append([]string(nil), vv...)
	}
	return out
}

func isCredentialHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie",
		"X-Api-Key", "Api-Key", "Openai-Api-Key", "X-Auth-Token",
		"X-Goog-Api-Key", "Anthropic-Api-Key":
		return true
	}
	return false
}
