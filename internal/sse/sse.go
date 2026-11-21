// Package sse incrementally parses server-sent events.
//
// Cake relays provider bytes unchanged; the parser only observes the stream
// so the proxy can show events while a response is still arriving.
package sse

import (
	"bytes"
	"strings"
)

// Event is one dispatched server-sent event. Data joins multiple data lines
// with a newline, as the SSE specification requires.
type Event struct {
	Name string
	Data string
}

// Parser consumes arbitrary byte chunks and dispatches complete events.
//
// A Parser is not safe for concurrent use; the caller feeds it from the single
// response relay loop.
type Parser struct {
	buf  []byte
	name string
	data []string
}

// Feed appends chunk and dispatches every event completed by it.
func (p *Parser) Feed(chunk []byte, emit func(Event)) {
	p.buf = append(p.buf, chunk...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			return
		}
		line := p.buf[:i]
		p.buf = p.buf[i+1:]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		p.line(string(line), emit)
	}
}

// Flush dispatches an unterminated final event and resets parser state.
func (p *Parser) Flush(emit func(Event)) {
	if len(p.buf) > 0 {
		line := string(p.buf)
		p.buf = nil
		line = strings.TrimSuffix(line, "\r")
		p.line(line, emit)
	}
	p.dispatch(emit)
}

func (p *Parser) line(line string, emit func(Event)) {
	if line == "" {
		p.dispatch(emit)
		return
	}
	if strings.HasPrefix(line, ":") {
		return
	}
	field, value, _ := strings.Cut(line, ":")
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "event":
		p.name = value
	case "data":
		p.data = append(p.data, value)
	}
}

func (p *Parser) dispatch(emit func(Event)) {
	if len(p.data) > 0 {
		emit(Event{Name: p.name, Data: strings.Join(p.data, "\n")})
	}
	p.name = ""
	p.data = p.data[:0]
}
