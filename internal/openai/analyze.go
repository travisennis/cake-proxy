// Package openai derives display fields (model, usage, finish reason, output
// text) from Chat Completions and Responses API payloads. Unknown shapes are
// ignored; the proxy already relays the payload byte for byte.
package openai

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/travisennis/cake-proxy/internal/capture"
)

const (
	APIChatCompletions = "chat_completions"
	APIResponses       = "responses"
	APIOther           = "other"

	outputTextLimit = 256 << 10
)

// Analysis is the best-effort summary of one provider response.
type Analysis struct {
	Model        string
	FinishReason string
	OutputText   string
	Usage        *capture.Usage
}

// ProbeRequest extracts the fields the proxy needs before forwarding.
func ProbeRequest(body []byte) (model string, stream bool) {
	root, ok := decode(body)
	if !ok {
		return "", false
	}
	m := asMap(root)
	return str(m["model"]), asBool(m["stream"])
}

// Analyze summarizes a complete response, from a non-streaming body or from
// the events captured off a stream.
func Analyze(api string, body capture.Body, events []capture.Event) Analysis {
	var analysis Analysis
	switch api {
	case APIChatCompletions:
		if len(body.JSON) > 0 {
			analysis = analyzeChatBody(body.JSON)
		} else {
			analysis = analyzeChatEvents(events)
		}
	case APIResponses:
		if len(body.JSON) > 0 {
			analysis = analyzeResponsesBody(body.JSON)
		} else {
			analysis = analyzeResponsesEvents(events)
		}
	}
	analysis.OutputText = truncateText(analysis.OutputText)
	return analysis
}

func analyzeChatBody(raw json.RawMessage) Analysis {
	root, ok := decode(raw)
	if !ok {
		return Analysis{}
	}
	obj := asMap(root)
	analysis := Analysis{
		Model: str(obj["model"]),
		Usage: chatUsage(obj["usage"]),
	}
	choices := asSlice(obj["choices"])
	if len(choices) > 0 {
		choice := asMap(choices[0])
		analysis.FinishReason = str(choice["finish_reason"])
		if message := asMap(choice["message"]); message != nil {
			var text strings.Builder
			appendContent(&text, message["content"])
			analysis.OutputText = text.String()
		}
	}
	return analysis
}

func analyzeChatEvents(events []capture.Event) Analysis {
	var analysis Analysis
	var text strings.Builder
	for _, event := range events {
		data, ok := event.Data.(map[string]any)
		if !ok {
			continue
		}
		if model := str(data["model"]); model != "" {
			analysis.Model = model
		}
		if usage := chatUsage(data["usage"]); usage != nil {
			analysis.Usage = usage
		}
		for _, rawChoice := range asSlice(data["choices"]) {
			choice := asMap(rawChoice)
			if finish := str(choice["finish_reason"]); finish != "" {
				analysis.FinishReason = finish
			}
			if delta := asMap(choice["delta"]); delta != nil {
				appendContent(&text, delta["content"])
			}
		}
	}
	analysis.OutputText = text.String()
	return analysis
}

func analyzeResponsesBody(raw json.RawMessage) Analysis {
	root, ok := decode(raw)
	if !ok {
		return Analysis{}
	}
	obj := asMap(root)
	analysis := Analysis{
		Model: str(obj["model"]),
		Usage: responsesUsage(obj["usage"]),
	}
	analysis.FinishReason = responseStatus(obj)
	if len(asSlice(obj["output"])) > 0 {
		var text strings.Builder
		for _, item := range asSlice(obj["output"]) {
			appendResponseItem(&text, item)
		}
		analysis.OutputText = text.String()
	}
	if errorObject := asMap(obj["error"]); errorObject != nil && str(errorObject["message"]) != "" {
		analysis.FinishReason = "failed"
	}
	return analysis
}

func analyzeResponsesEvents(events []capture.Event) Analysis {
	var analysis Analysis
	var text strings.Builder
	var fallback strings.Builder
	sawDelta := false

	for _, event := range events {
		data, ok := event.Data.(map[string]any)
		if !ok {
			continue
		}
		switch event.Name {
		case "response.created", "response.in_progress", "response.completed",
			"response.incomplete", "response.failed":
			response := asMap(data["response"])
			if response == nil {
				response = data
			}
			if model := str(response["model"]); model != "" {
				analysis.Model = model
			}
			if usage := responsesUsage(response["usage"]); usage != nil {
				analysis.Usage = usage
			}
			if status := responseStatus(response); status != "" {
				analysis.FinishReason = status
			}
			if errorObject := asMap(response["error"]); errorObject != nil && str(errorObject["message"]) != "" {
				analysis.FinishReason = "failed"
			}
		case "response.output_text.delta":
			if delta := str(data["delta"]); delta != "" {
				text.WriteString(delta)
				sawDelta = true
			}
		case "response.refusal.delta":
			if delta := str(data["delta"]); delta != "" {
				text.WriteString(delta)
				sawDelta = true
			}
		case "response.output_text.done":
			if !sawDelta {
				text.WriteString(str(data["text"]))
			}
		case "response.output_item.done":
			if !sawDelta {
				appendResponseItem(&fallback, data["item"])
			}
		}
	}
	if text.Len() > 0 {
		analysis.OutputText = text.String()
	} else {
		analysis.OutputText = fallback.String()
	}
	return analysis
}

func responseStatus(obj map[string]any) string {
	if details := asMap(obj["incomplete_details"]); details != nil {
		if reason := str(details["reason"]); reason != "" {
			return reason
		}
	}
	return str(obj["status"])
}

func appendContent(text *strings.Builder, content any) {
	switch value := content.(type) {
	case string:
		text.WriteString(value)
	case []any:
		for _, rawPart := range value {
			part := asMap(rawPart)
			if part == nil {
				continue
			}
			if partText := str(part["text"]); partText != "" {
				text.WriteString(partText)
			}
		}
	}
}

func appendResponseItem(text *strings.Builder, rawItem any) {
	item := asMap(rawItem)
	if item == nil || str(item["type"]) != "message" {
		return
	}
	for _, rawPart := range asSlice(item["content"]) {
		part := asMap(rawPart)
		if part == nil {
			continue
		}
		if part["type"] == "output_text" || part["type"] == "text" {
			text.WriteString(str(part["text"]))
		}
		if refusal := str(part["refusal"]); refusal != "" {
			text.WriteString(refusal)
		}
	}
}

func chatUsage(raw any) *capture.Usage {
	obj := asMap(raw)
	if obj == nil {
		return nil
	}
	usage := &capture.Usage{
		InputTokens:  integer(obj["prompt_tokens"]),
		OutputTokens: integer(obj["completion_tokens"]),
		TotalTokens:  integer(obj["total_tokens"]),
	}
	if details := asMap(obj["prompt_tokens_details"]); details != nil {
		usage.CachedInputTokens = integer(details["cached_tokens"])
	}
	if details := asMap(obj["completion_tokens_details"]); details != nil {
		usage.ReasoningTokens = integer(details["reasoning_tokens"])
	}
	if *usage == (capture.Usage{}) {
		return nil
	}
	return usage
}

func responsesUsage(raw any) *capture.Usage {
	obj := asMap(raw)
	if obj == nil {
		return nil
	}
	usage := &capture.Usage{
		InputTokens:  firstInteger(obj, "input_tokens", "prompt_tokens"),
		OutputTokens: firstInteger(obj, "output_tokens", "completion_tokens"),
		TotalTokens:  integer(obj["total_tokens"]),
	}
	if details := asMap(obj["input_tokens_details"]); details != nil {
		usage.CachedInputTokens = integer(details["cached_tokens"])
	} else if details := asMap(obj["prompt_tokens_details"]); details != nil {
		usage.CachedInputTokens = integer(details["cached_tokens"])
	}
	if details := asMap(obj["output_tokens_details"]); details != nil {
		usage.ReasoningTokens = integer(details["reasoning_tokens"])
	} else if details := asMap(obj["completion_tokens_details"]); details != nil {
		usage.ReasoningTokens = integer(details["reasoning_tokens"])
	}
	if *usage == (capture.Usage{}) {
		return nil
	}
	return usage
}

func firstInteger(obj map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value := integer(obj[key]); value != 0 {
			return value
		}
	}
	return 0
}

func decode(raw []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}

func asMap(value any) map[string]any {
	obj, _ := value.(map[string]any)
	return obj
}

func asSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func str(value any) string {
	text, _ := value.(string)
	return text
}

func asBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func integer(value any) int64 {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return 0
		}
		return parsed
	case float64:
		return int64(number)
	case int64:
		return number
	case int:
		return int64(number)
	}
	return 0
}

func truncateText(text string) string {
	if len(text) <= outputTextLimit {
		return text
	}
	cut := outputTextLimit
	for cut > 0 && !utf8Start(text[cut]) {
		cut--
	}
	return text[:cut] + "\n… [truncated]"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
