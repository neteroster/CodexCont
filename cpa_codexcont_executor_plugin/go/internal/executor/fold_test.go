package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeRoundReader struct {
	chunks [][]byte
	idx    int
	err    error
}

func (r *fakeRoundReader) Read(context.Context) ([]byte, bool, error) {
	if r.err != nil {
		return nil, false, r.err
	}
	if r.idx >= len(r.chunks) {
		return nil, true, nil
	}
	chunk := r.chunks[r.idx]
	r.idx++
	return chunk, r.idx >= len(r.chunks), nil
}

func (r *fakeRoundReader) Close() error { return nil }

func round(events ...map[string]any) [][]byte {
	var out [][]byte
	for _, ev := range events {
		out = append(out, SerializeEvent(ev))
	}
	return out
}

func created(id string) map[string]any {
	return map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress"}}
}

func reasoning(id string, encrypted bool) []map[string]any {
	item := map[string]any{"id": id, "type": "reasoning", "content": []any{}}
	if encrypted {
		item["encrypted_content"] = "enc-" + id
	}
	return []map[string]any{
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
	}
}

func message(index int, text string) []map[string]any {
	item := map[string]any{"id": "msg-" + text, "type": "message", "role": "assistant"}
	return []map[string]any{
		{"type": "response.output_item.added", "output_index": index, "item": item},
		{"type": "response.output_text.delta", "output_index": index, "item_id": item["id"], "content_index": 0, "delta": text},
		{"type": "response.output_item.done", "output_index": index, "item": item},
	}
}

func completed(reasoningTokens, outputTokens int) map[string]any {
	return map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":           "resp-terminal",
			"status":       "completed",
			"output":       []any{},
			"input_tokens": 0,
			"usage": map[string]any{
				"input_tokens":  100,
				"output_tokens": outputTokens,
				"total_tokens":  100 + outputTokens,
				"input_tokens_details": map[string]any{
					"cached_tokens": 20,
				},
				"output_tokens_details": map[string]any{
					"reasoning_tokens": reasoningTokens,
				},
			},
		},
	}
}

func parseEvents(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	parser := &SSEParser{}
	var out []map[string]any
	for _, ev := range parser.Feed(raw) {
		if ev.Data != nil {
			out = append(out, ev.Data)
		}
	}
	for _, ev := range parser.Close() {
		if ev.Data != nil {
			out = append(out, ev.Data)
		}
	}
	return out
}

func terminalEvent(t *testing.T, events []map[string]any) map[string]any {
	t.Helper()
	for _, ev := range events {
		if terminalTypes[toString(ev["type"])] {
			return ev
		}
	}
	t.Fatal("terminal event not found")
	return nil
}

func TestFoldStreamAutoContinuesAndReconstructsTerminal(t *testing.T) {
	cfg := DefaultConfig()
	base := map[string]any{"model": "gpt-5.5", "stream": true, "input": []any{map[string]any{"role": "user", "content": "hi"}}}
	first := []map[string]any{created("resp-a")}
	first = append(first, reasoning("rs-a", true)...)
	first = append(first, message(1, "BAD")...)
	first = append(first, completed(516, 536))
	second := []map[string]any{created("resp-b")}
	second = append(second, reasoning("rs-b", true)...)
	second = append(second, message(1, "GOOD")...)
	second = append(second, completed(120, 150))
	rounds := map[int][][]byte{1: round(first...), 2: round(second...)}
	var opened [][]byte
	var emitted bytes.Buffer
	result, err := FoldStream(context.Background(), cfg, base, func(_ context.Context, body []byte, roundNo int) (StreamReader, error) {
		opened = append(opened, body)
		return &fakeRoundReader{chunks: rounds[roundNo]}, nil
	}, func(_ context.Context, payload []byte) error {
		_, _ = emitted.Write(payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Protection != "auto_continued" || result.Summary["continuation_count"] != 1 {
		t.Fatalf("summary = %#v", result.Summary)
	}
	if len(opened) != 2 {
		t.Fatalf("opened rounds = %d", len(opened))
	}
	if strings.Contains(emitted.String(), "BAD") || !strings.Contains(emitted.String(), "GOOD") {
		t.Fatalf("folded stream leaked/truncated output incorrectly:\n%s", emitted.String())
	}
	var secondBody map[string]any
	if err := json.Unmarshal(opened[1], &secondBody); err != nil {
		t.Fatal(err)
	}
	input := secondBody["input"].([]any)
	if len(input) < 3 {
		t.Fatalf("continuation input too short: %#v", input)
	}
	if !strings.Contains(string(opened[1]), EncryptedInclude) || !strings.Contains(string(opened[1]), cfg.MarkerText) {
		t.Fatalf("continuation payload missing encrypted include or marker: %s", string(opened[1]))
	}
	events := parseEvents(t, emitted.Bytes())
	term := terminalEvent(t, events)
	resp := mapValue(term, "response")
	md := mapValue(resp, "metadata")
	if got := len(md["proxy_rounds"].([]any)); got != 2 {
		t.Fatalf("proxy_rounds len = %d metadata=%#v", got, md)
	}
	usage := mapValue(resp, "usage")
	if usage["output_tokens"] != float64(666) {
		t.Fatalf("agent output usage = %#v", usage)
	}
	assertSequenceMonotonic(t, events)
}

func TestFoldStreamFirstRoundPreservesStringInput(t *testing.T) {
	cfg := DefaultConfig()
	base := map[string]any{"model": "gpt-5.5", "stream": true, "input": "say hi"}
	events := []map[string]any{created("resp-a"), completed(42, 60)}
	var opened [][]byte
	var emitted bytes.Buffer
	_, err := FoldStream(context.Background(), cfg, base, func(_ context.Context, body []byte, _ int) (StreamReader, error) {
		opened = append(opened, body)
		return &fakeRoundReader{chunks: round(events...)}, nil
	}, func(_ context.Context, payload []byte) error {
		_, _ = emitted.Write(payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 {
		t.Fatalf("opened rounds = %d", len(opened))
	}
	var firstBody map[string]any
	if err := json.Unmarshal(opened[0], &firstBody); err != nil {
		t.Fatal(err)
	}
	if firstBody["input"] != "say hi" {
		t.Fatalf("first round input was rewritten: %#v", firstBody["input"])
	}
	if !strings.Contains(emitted.String(), "response.completed") {
		t.Fatalf("terminal missing:\n%s", emitted.String())
	}
}

func TestFoldStreamAcceptsLineChunkedSSE(t *testing.T) {
	cfg := DefaultConfig()
	base := map[string]any{"model": "gpt-5.5", "stream": true, "input": []any{}}
	events := []map[string]any{created("resp-a"), completed(42, 60)}
	var chunks [][]byte
	for _, ev := range events {
		raw := SerializeEvent(ev)
		for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			chunks = append(chunks, []byte(line))
		}
	}
	var emitted bytes.Buffer
	result, err := FoldStream(context.Background(), cfg, base, func(context.Context, []byte, int) (StreamReader, error) {
		return &fakeRoundReader{chunks: chunks}, nil
	}, func(_ context.Context, payload []byte) error {
		_, _ = emitted.Write(payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Protection != "protected_clean" {
		t.Fatalf("protection=%s summary=%#v stream=%s", result.Protection, result.Summary, emitted.String())
	}
	term := terminalEvent(t, parseEvents(t, emitted.Bytes()))
	if term["type"] != "response.completed" {
		t.Fatalf("terminal = %#v", term)
	}
}

func TestFoldStreamMissingEncryptedDoesNotContinue(t *testing.T) {
	cfg := DefaultConfig()
	base := map[string]any{"model": "gpt-5.5", "stream": true, "input": []any{}}
	events := []map[string]any{created("resp-a")}
	events = append(events, reasoning("rs-a", false)...)
	events = append(events, message(1, "VISIBLE")...)
	events = append(events, completed(516, 536))
	var opened int
	var emitted bytes.Buffer
	result, err := FoldStream(context.Background(), cfg, base, func(context.Context, []byte, int) (StreamReader, error) {
		opened++
		return &fakeRoundReader{chunks: round(events...)}, nil
	}, func(_ context.Context, payload []byte) error {
		_, _ = emitted.Write(payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened != 1 || result.Protection != "risk_uncontinued" {
		t.Fatalf("opened=%d summary=%#v", opened, result.Summary)
	}
	term := terminalEvent(t, parseEvents(t, emitted.Bytes()))
	md := mapValue(mapValue(term, "response"), "metadata")
	if md["proxy_stopped_reason"] != "no_encrypted_content" || !strings.Contains(emitted.String(), "VISIBLE") {
		t.Fatalf("metadata/output = %#v\n%s", md, emitted.String())
	}
}

func TestFoldStreamEOFEmitsIncompleteAndDropsBufferedOutput(t *testing.T) {
	cfg := DefaultConfig()
	base := map[string]any{"model": "gpt-5.5", "stream": true, "input": []any{}}
	events := []map[string]any{created("resp-a")}
	events = append(events, message(0, "HALF")...)
	var emitted bytes.Buffer
	result, err := FoldStream(context.Background(), cfg, base, func(context.Context, []byte, int) (StreamReader, error) {
		return &fakeRoundReader{chunks: round(events...)}, nil
	}, func(_ context.Context, payload []byte) error {
		_, _ = emitted.Write(payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Protection != "incomplete" || strings.Contains(emitted.String(), "HALF") {
		t.Fatalf("result=%#v stream=%s", result.Summary, emitted.String())
	}
	term := terminalEvent(t, parseEvents(t, emitted.Bytes()))
	if term["type"] != "response.incomplete" {
		t.Fatalf("terminal = %#v", term)
	}
}

func TestFoldStreamMaxContinueStopsWithMetadata(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxContinue = 1
	base := map[string]any{"model": "gpt-5.5", "stream": true, "input": []any{}}
	r1 := []map[string]any{created("resp-a")}
	r1 = append(r1, reasoning("rs-a", true)...)
	r1 = append(r1, completed(516, 516))
	r2 := []map[string]any{created("resp-b")}
	r2 = append(r2, reasoning("rs-b", true)...)
	r2 = append(r2, completed(1034, 1034))
	rounds := map[int][][]byte{1: round(r1...), 2: round(r2...)}
	var opened int
	var emitted bytes.Buffer
	_, err := FoldStream(context.Background(), cfg, base, func(_ context.Context, _ []byte, roundNo int) (StreamReader, error) {
		opened++
		return &fakeRoundReader{chunks: rounds[roundNo]}, nil
	}, func(_ context.Context, payload []byte) error {
		_, _ = emitted.Write(payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened != 2 {
		t.Fatalf("opened = %d", opened)
	}
	term := terminalEvent(t, parseEvents(t, emitted.Bytes()))
	md := mapValue(mapValue(term, "response"), "metadata")
	if md["proxy_stopped_reason"] != "max_continue" {
		t.Fatalf("metadata = %#v", md)
	}
}

func TestFoldStreamContinuationOpenErrorEmitsIncomplete(t *testing.T) {
	cfg := DefaultConfig()
	base := map[string]any{"model": "gpt-5.5", "stream": true, "input": []any{}}
	r1 := []map[string]any{created("resp-a")}
	r1 = append(r1, reasoning("rs-a", true)...)
	r1 = append(r1, completed(516, 516))
	var opened int
	var emitted bytes.Buffer
	result, err := FoldStream(context.Background(), cfg, base, func(_ context.Context, _ []byte, roundNo int) (StreamReader, error) {
		opened++
		if roundNo == 2 {
			return nil, errors.New("upstream unavailable")
		}
		return &fakeRoundReader{chunks: round(r1...)}, nil
	}, func(_ context.Context, payload []byte) error {
		_, _ = emitted.Write(payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened != 2 || result.Protection != "failed" {
		t.Fatalf("opened=%d summary=%#v", opened, result.Summary)
	}
	term := terminalEvent(t, parseEvents(t, emitted.Bytes()))
	resp := mapValue(term, "response")
	if term["type"] != "response.incomplete" || mapValue(resp, "incomplete_details")["reason"] != "upstream_error" {
		t.Fatalf("terminal = %#v", term)
	}
}

func assertSequenceMonotonic(t *testing.T, events []map[string]any) {
	t.Helper()
	prev := -1
	for _, ev := range events {
		raw, ok := intValue(ev["sequence_number"])
		if !ok {
			continue
		}
		if int(raw) <= prev {
			t.Fatalf("sequence not monotonic after %d: %#v", prev, ev)
		}
		prev = int(raw)
	}
}
