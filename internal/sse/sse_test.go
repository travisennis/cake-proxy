package sse

import (
	"reflect"
	"testing"
)

func TestParserSplitsAcrossChunks(t *testing.T) {
	var parser Parser
	var events []Event
	emit := func(event Event) { events = append(events, event) }

	parser.Feed([]byte("event: response.output_text.delta\nda"), emit)
	if len(events) != 0 {
		t.Fatalf("dispatched before the event ended: %#v", events)
	}
	parser.Feed([]byte("ta: {\"delta\":\"hi\"}\n\n"), emit)

	want := []Event{{Name: "response.output_text.delta", Data: `{"delta":"hi"}`}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestParserJoinsDataLinesAndKeepsCRLF(t *testing.T) {
	var parser Parser
	var events []Event
	parser.Feed([]byte("data: one\r\ndata: two\r\n\r\n"), func(event Event) {
		events = append(events, event)
	})

	want := []Event{{Data: "one\ntwo"}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestParserIgnoresCommentsAndOtherFields(t *testing.T) {
	var parser Parser
	var events []Event
	parser.Feed([]byte(": keep-alive\nid: 7\nretry: 10\ndata: payload\n\n"), func(event Event) {
		events = append(events, event)
	})

	want := []Event{{Data: "payload"}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestParserDoesNotDispatchEmptyEvent(t *testing.T) {
	var parser Parser
	called := false
	parser.Feed([]byte("\n\n"), func(Event) { called = true })
	if called {
		t.Fatal("empty event dispatched")
	}
}

func TestParserFlush(t *testing.T) {
	var parser Parser
	var events []Event
	parser.Feed([]byte("event: done\ndata: [DONE]"), func(event Event) {
		events = append(events, event)
	})
	if len(events) != 0 {
		t.Fatalf("dispatched before flush: %#v", events)
	}
	parser.Flush(func(event Event) { events = append(events, event) })

	want := []Event{{Name: "done", Data: "[DONE]"}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}
