package openai

import (
	"testing"

	"github.com/travisennis/cake-proxy/internal/capture"
)

func TestProbeRequest(t *testing.T) {
	model, stream := ProbeRequest([]byte(`{"model":"deepseek-v4.1-flash","stream":true}`))
	if model != "deepseek-v4.1-flash" || !stream {
		t.Fatalf("ProbeRequest = %q, %v", model, stream)
	}
}

func TestAnalyzeChatBody(t *testing.T) {
	body := capture.FromBytes([]byte(`{
		"id": "chatcmpl-1",
		"model": "deepseek-v4.1-flash",
		"choices": [{"message": {"role": "assistant", "content": "Hello"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 120, "completion_tokens": 4, "total_tokens": 124,
		          "prompt_tokens_details": {"cached_tokens": 20},
		          "completion_tokens_details": {"reasoning_tokens": 2}}
	}`))

	analysis := Analyze(APIChatCompletions, body, nil)
	if analysis.Model != "deepseek-v4.1-flash" {
		t.Fatalf("model = %q", analysis.Model)
	}
	if analysis.OutputText != "Hello" {
		t.Fatalf("output = %q", analysis.OutputText)
	}
	if analysis.FinishReason != "stop" {
		t.Fatalf("finish = %q", analysis.FinishReason)
	}
	if analysis.Usage == nil || analysis.Usage.InputTokens != 120 ||
		analysis.Usage.OutputTokens != 4 || analysis.Usage.CachedInputTokens != 20 ||
		analysis.Usage.ReasoningTokens != 2 {
		t.Fatalf("usage = %#v", analysis.Usage)
	}
}

func TestAnalyzeChatEvents(t *testing.T) {
	events := []capture.Event{
		{Data: map[string]any{
			"model":   "deepseek-v4.1-flash",
			"choices": []any{map[string]any{"delta": map[string]any{"content": "Hel"}}},
		}},
		{Data: map[string]any{
			"choices": []any{map[string]any{
				"delta":         map[string]any{"content": "lo"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     float64(10),
				"completion_tokens": float64(2),
				"total_tokens":      float64(12),
			},
		}},
	}

	analysis := Analyze(APIChatCompletions, capture.Body{}, events)
	if analysis.Model != "deepseek-v4.1-flash" || analysis.OutputText != "Hello" {
		t.Fatalf("analysis = %#v", analysis)
	}
	if analysis.FinishReason != "stop" {
		t.Fatalf("finish = %q", analysis.FinishReason)
	}
	if analysis.Usage == nil || analysis.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %#v", analysis.Usage)
	}
}

func TestAnalyzeResponsesBody(t *testing.T) {
	body := capture.FromBytes([]byte(`{
		"model": "gpt-5",
		"status": "completed",
		"output": [
			{"type": "reasoning", "summary": [{"type": "summary_text", "text": "think"}]},
			{"type": "message", "role": "assistant", "content": [
				{"type": "output_text", "text": "Hello "},
				{"type": "output_text", "text": "there"}
			]},
			{"type": "function_call", "name": "Bash", "arguments": "{}", "call_id": "c1"}
		],
		"usage": {"input_tokens": 50, "output_tokens": 7, "total_tokens": 57}
	}`))

	analysis := Analyze(APIResponses, body, nil)
	if analysis.Model != "gpt-5" || analysis.OutputText != "Hello there" {
		t.Fatalf("analysis = %#v", analysis)
	}
	if analysis.FinishReason != "completed" {
		t.Fatalf("finish = %q", analysis.FinishReason)
	}
	if analysis.Usage == nil || analysis.Usage.InputTokens != 50 || analysis.Usage.TotalTokens != 57 {
		t.Fatalf("usage = %#v", analysis.Usage)
	}
}

func TestAnalyzeResponsesEvents(t *testing.T) {
	events := []capture.Event{
		{Name: "response.created", Data: map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"model":  "gpt-5",
				"status": "in_progress",
			},
		}},
		{Name: "response.output_text.delta", Data: map[string]any{
			"delta": "Hel",
		}},
		{Name: "response.output_text.delta", Data: map[string]any{
			"delta": "lo",
		}},
		{Name: "response.completed", Data: map[string]any{
			"response": map[string]any{
				"model":  "gpt-5",
				"status": "completed",
				"usage": map[string]any{
					"input_tokens":  float64(9),
					"output_tokens": float64(2),
					"total_tokens":  float64(11),
				},
			},
		}},
	}

	analysis := Analyze(APIResponses, capture.Body{}, events)
	if analysis.Model != "gpt-5" || analysis.OutputText != "Hello" {
		t.Fatalf("analysis = %#v", analysis)
	}
	if analysis.FinishReason != "completed" {
		t.Fatalf("finish = %q", analysis.FinishReason)
	}
	if analysis.Usage == nil || analysis.Usage.InputTokens != 9 || analysis.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %#v", analysis.Usage)
	}
}

func TestAnalyzeResponsesIncompleteReason(t *testing.T) {
	body := capture.FromBytes([]byte(`{
		"model": "gpt-5",
		"status": "incomplete",
		"incomplete_details": {"reason": "max_output_tokens"},
		"output": []
	}`))
	analysis := Analyze(APIResponses, body, nil)
	if analysis.FinishReason != "max_output_tokens" {
		t.Fatalf("finish = %q", analysis.FinishReason)
	}
}
