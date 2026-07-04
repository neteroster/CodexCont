package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var terminalTypes = map[string]bool{
	"response.completed":  true,
	"response.failed":     true,
	"response.incomplete": true,
}

type StreamReader interface {
	Read(context.Context) ([]byte, bool, error)
	Close() error
}

type StreamOpener func(context.Context, []byte, int) (StreamReader, error)
type Emitter func(context.Context, []byte) error

type FoldResult struct {
	RequestID  string
	Protection string
	Summary    map[string]any
}

type seqCounter struct {
	n int
}

func (s *seqCounter) Next() int {
	v := s.n
	s.n++
	return v
}

type bufferEntry struct {
	oi       string
	itemType string
	events   []map[string]any
	item     map[string]any
}

func FoldStream(ctx context.Context, cfg Config, baseBody map[string]any, open StreamOpener, emit Emitter) (*FoldResult, error) {
	cfg = cfg.Normalize()
	startedAt := time.Now()
	displayModel := toString(baseBody["model"])
	origInput := anyList(baseBody["input"])
	replayTail := []any{}
	finalOutput := []any{}
	totalUsage := map[string]any{}
	var firstUsage map[string]any
	roundsInfo := []map[string]any{}
	seq := &seqCounter{}
	dsOI := 0
	sawDone := false
	continuationCount := 0
	var firstTruncRound any
	var firstTruncTokens any
	var firstTruncN any
	var firstTruncDecision string
	var latestReasoning any
	var finalStatus string
	var stoppedReason string
	var failureReason string
	var baseResponse map[string]any
	var requestID string

	firstPayload := BuildFirstPayload(baseBody)
	firstRaw, err := marshalPayload(firstPayload)
	if err != nil {
		return nil, err
	}
	reader, err := open(ctx, firstRaw, 1)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	roundNo := 0
	for {
		roundNo++
		itemKind := map[string]string{}
		oiMap := map[string]int{}
		var outBuffer []bufferEntry
		var roundReasoning []map[string]any
		var terminal map[string]any
		var usage map[string]any
		parser := &SSEParser{}

		readTerminal := false
		for !readTerminal {
			chunk, done, readErr := reader.Read(ctx)
			if readErr != nil {
				failureReason = "upstream_stream_error"
				return emitIncomplete(ctx, emit, baseResponse, finalOutput, agentUsage(firstUsage, totalUsage, nil, false), seq, "upstream_error", roundsInfo, totalUsage, startedAt, requestID, failureReason)
			}
			events := parser.Feed(chunk)
			if done {
				events = append(events, parser.Close()...)
			}
			for _, framed := range events {
				if framed.Done {
					sawDone = true
					continue
				}
				ev := cloneEvent(framed.Data)
				typ := toString(ev["type"])
				if typ == "response.created" || typ == "response.in_progress" {
					if roundNo == 1 {
						if displayModel != "" {
							if resp := mapValue(ev, "response"); resp != nil {
								resp["model"] = displayModel
							}
						}
						if typ == "response.created" {
							baseResponse = mapValue(ev, "response")
							requestID = firstString(baseResponse["id"], requestID)
						}
						ev["sequence_number"] = seq.Next()
						if err := emit(ctx, SerializeEvent(ev)); err != nil {
							return nil, err
						}
					}
					continue
				}
				if terminalTypes[typ] {
					terminal = ev
					usage = mapValue(mapValue(ev, "response"), "usage")
					readTerminal = true
					break
				}
				upKey := oiKey(ev["output_index"])
				if typ == "response.output_item.added" {
					item := mapValue(ev, "item")
					itemType := toString(item["type"])
					if itemType == "reasoning" {
						itemKind[upKey] = "reasoning"
						oiMap[upKey] = dsOI
						ev["output_index"] = dsOI
						dsOI++
						ev["sequence_number"] = seq.Next()
						if err := emit(ctx, SerializeEvent(ev)); err != nil {
							return nil, err
						}
					} else {
						itemKind[upKey] = "buffered"
						outBuffer = append(outBuffer, bufferEntry{
							oi:       upKey,
							itemType: itemType,
							events:   []map[string]any{ev},
							item:     item,
						})
					}
					continue
				}
				switch itemKind[upKey] {
				case "reasoning":
					if mapped, ok := oiMap[upKey]; ok {
						ev["output_index"] = mapped
					}
					ev["sequence_number"] = seq.Next()
					if typ == "response.output_item.done" {
						item := mapValue(ev, "item")
						roundReasoning = append(roundReasoning, item)
						finalOutput = append(finalOutput, item)
					}
					if err := emit(ctx, SerializeEvent(ev)); err != nil {
						return nil, err
					}
				case "buffered":
					if entry := findBuffer(outBuffer, upKey); entry >= 0 {
						outBuffer[entry].events = append(outBuffer[entry].events, ev)
						if typ == "response.output_item.done" {
							item := mapValue(ev, "item")
							if len(item) > 0 {
								outBuffer[entry].item = item
							}
						}
					}
				default:
					ev["sequence_number"] = seq.Next()
					if err := emit(ctx, SerializeEvent(ev)); err != nil {
						return nil, err
					}
				}
			}
			if done || readTerminal {
				break
			}
		}

		_ = reader.Close()
		sawTerminal := terminal != nil
		sumUsage(totalUsage, usage)
		if roundNo == 1 {
			firstUsage = usage
		}
		rt, rtOK := ReasoningTokens(usage)
		if rtOK {
			latestReasoning = rt
		}
		n, nOK := TierN(rt, rtOK, cfg.TruncationStep)
		roundInfo := map[string]any{"round": roundNo}
		if rtOK {
			roundInfo["reasoning_tokens"] = rt
		}
		if nOK {
			roundInfo["n"] = n
		}

		hasEncrypted := hasReplayableEncrypted(roundReasoning)
		withinCaps := cfg.MaxTotalOutputTokens <= 0 || usageOutputTokens(totalUsage) < int64(cfg.MaxTotalOutputTokens)
		doContinue := cfg.Enabled &&
			sawTerminal &&
			ShouldContinue(rt, rtOK, cfg.MinN, cfg.MaxN, cfg.TruncationStep) &&
			hasEncrypted &&
			roundNo <= cfg.MaxContinue &&
			withinCaps

		decision := "clean"
		if doContinue {
			decision = "continue"
		} else if !sawTerminal {
			decision = "upstream_eof"
			stoppedReason = "upstream_eof"
		} else if IsTruncationPattern(rt, rtOK, cfg.TruncationStep) {
			switch {
			case !hasEncrypted:
				decision = "no_encrypted_content"
				stoppedReason = "no_encrypted_content"
			case roundNo > cfg.MaxContinue:
				decision = "max_continue"
				stoppedReason = "max_continue"
			case !withinCaps:
				decision = "max_total_output_tokens"
				stoppedReason = "max_total_output_tokens"
			default:
				decision = "tier_out_of_window"
				stoppedReason = "tier_out_of_window"
			}
		}
		roundInfo["decision"] = decision
		roundInfo["truncation_match"] = IsTruncationPattern(rt, rtOK, cfg.TruncationStep)
		roundInfo["buffered"] = bufferedTypes(outBuffer)
		roundsInfo = append(roundsInfo, roundInfo)
		if IsTruncationPattern(rt, rtOK, cfg.TruncationStep) && firstTruncRound == nil {
			firstTruncRound = roundNo
			firstTruncTokens = rt
			if nOK {
				firstTruncN = n
			}
			firstTruncDecision = decision
		}

		if doContinue {
			continuationCount++
			lastReasoning := roundReasoning[len(roundReasoning)-1]
			replayTail = append(replayTail, mapsToAny(roundReasoning)...)
			replayTail = append(replayTail, CommentaryMessage(cfg.MarkerText))
			nextInput := append([]any{}, origInput...)
			nextInput = append(nextInput, replayTail...)
			nextPayload := BuildRoundPayload(baseBody, nextInput, cfg, true)
			nextRaw, err := marshalPayload(nextPayload)
			if err != nil {
				return nil, err
			}
			_ = lastReasoning
			reader, err = open(ctx, nextRaw, roundNo+1)
			if err != nil {
				failureReason = "continuation_upstream_error"
				return emitIncomplete(ctx, emit, baseResponse, finalOutput, agentUsage(firstUsage, totalUsage, usage, false), seq, "upstream_error", roundsInfo, totalUsage, startedAt, requestID, failureReason)
			}
			continue
		}

		if !sawTerminal {
			finalStatus = "incomplete"
			return emitIncomplete(ctx, emit, baseResponse, finalOutput, agentUsage(firstUsage, totalUsage, usage, false), seq, "upstream_eof", roundsInfo, totalUsage, startedAt, requestID, "")
		}

		for _, entry := range outBuffer {
			for _, ev := range entry.events {
				if _, ok := ev["output_index"]; ok {
					ev["output_index"] = dsOI
				}
				ev["sequence_number"] = seq.Next()
				if err := emit(ctx, SerializeEvent(ev)); err != nil {
					return nil, err
				}
			}
			dsOI++
			if len(entry.item) > 0 {
				finalOutput = append(finalOutput, entry.item)
			}
		}
		resp := mapValue(terminal, "response")
		finalStatus = firstString(resp["status"], "completed")
		term := reconstructTerminal(terminal, baseResponse, finalOutput, agentUsage(firstUsage, totalUsage, usage, true), seq.Next(), roundsInfo, stoppedReason, totalUsage)
		if err := emit(ctx, SerializeEvent(term)); err != nil {
			return nil, err
		}
		if sawDone {
			if err := emit(ctx, SerializeDone()); err != nil {
				return nil, err
			}
		}
		protection := protectionValue(continuationCount, stoppedReason, failureReason, finalStatus)
		summary := summaryMap(requestID, baseBody, startedAt, protection, finalStatus, stoppedReason, failureReason, roundsInfo, latestReasoning, continuationCount, firstTruncRound, firstTruncTokens, firstTruncN, firstTruncDecision)
		return &FoldResult{RequestID: requestID, Protection: protection, Summary: summary}, nil
	}
}

func emitIncomplete(ctx context.Context, emit Emitter, baseResponse map[string]any, finalOutput []any, usage map[string]any, seq *seqCounter, reason string, rounds []map[string]any, totalUsage map[string]any, startedAt time.Time, requestID, failureReason string) (*FoldResult, error) {
	ev := syntheticIncomplete(baseResponse, finalOutput, usage, seq.Next(), reason, rounds, totalUsage)
	if err := emit(ctx, SerializeEvent(ev)); err != nil {
		return nil, err
	}
	protection := protectionValue(0, reason, failureReason, "incomplete")
	summary := summaryMap(requestID, nil, startedAt, protection, "incomplete", reason, failureReason, rounds, latestFromRounds(rounds), 0, nil, nil, nil, "")
	return &FoldResult{RequestID: requestID, Protection: protection, Summary: summary}, nil
}

func reconstructTerminal(terminal, baseResponse map[string]any, output []any, usage map[string]any, seq int, rounds []map[string]any, stoppedReason string, billedUsage map[string]any) map[string]any {
	resp := cloneMap(baseResponse)
	if len(resp) == 0 {
		resp = cloneMap(mapValue(terminal, "response"))
	}
	resp["output"] = output
	resp["usage"] = usage
	tresp := mapValue(terminal, "response")
	resp["status"] = firstString(tresp["status"], "completed")
	if details, ok := tresp["incomplete_details"]; ok {
		resp["incomplete_details"] = details
	}
	withProxyMetadata(resp, rounds, stoppedReason, billedUsage)
	typ := firstString(terminal["type"], "response.completed")
	return map[string]any{"type": typ, "response": resp, "sequence_number": seq}
}

func syntheticIncomplete(baseResponse map[string]any, output []any, usage map[string]any, seq int, reason string, rounds []map[string]any, billedUsage map[string]any) map[string]any {
	resp := cloneMap(baseResponse)
	resp["output"] = output
	resp["usage"] = usage
	resp["status"] = "incomplete"
	resp["incomplete_details"] = map[string]any{"reason": reason}
	withProxyMetadata(resp, rounds, reason, billedUsage)
	return map[string]any{"type": "response.incomplete", "response": resp, "sequence_number": seq}
}

func withProxyMetadata(resp map[string]any, rounds []map[string]any, stoppedReason string, billedUsage map[string]any) {
	md := mapValue(resp, "metadata")
	if md == nil {
		md = map[string]any{}
	}
	md["proxy_rounds"] = rounds
	if len(billedUsage) > 0 {
		md["proxy_billed_usage"] = billedUsage
	}
	if stoppedReason != "" {
		md["proxy_stopped_reason"] = stoppedReason
	}
	resp["metadata"] = md
}

func agentUsage(first, total, finalRound map[string]any, flushedFinal bool) map[string]any {
	inTok, _ := intValue(first["input_tokens"])
	cached, cachedOK := intValue(mapValue(first, "input_tokens_details")["cached_tokens"])
	reasoning, _ := intValue(mapValue(total, "output_tokens_details")["reasoning_tokens"])
	finalNonReason := int64(0)
	if flushedFinal && finalRound != nil {
		fo, _ := intValue(finalRound["output_tokens"])
		fr, _ := intValue(mapValue(finalRound, "output_tokens_details")["reasoning_tokens"])
		if fo > fr {
			finalNonReason = fo - fr
		}
	}
	outTok := reasoning + finalNonReason
	usage := map[string]any{
		"input_tokens":          inTok,
		"output_tokens":         outTok,
		"total_tokens":          inTok + outTok,
		"output_tokens_details": map[string]any{"reasoning_tokens": reasoning},
	}
	if cachedOK {
		usage["input_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	return usage
}

func sumUsage(acc map[string]any, usage map[string]any) {
	if usage == nil {
		return
	}
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if value, ok := intValue(usage[key]); ok {
			current, _ := intValue(acc[key])
			acc[key] = current + value
		}
	}
	if cached, ok := intValue(mapValue(usage, "input_tokens_details")["cached_tokens"]); ok {
		details := mapValue(acc, "input_tokens_details")
		if details == nil {
			details = map[string]any{}
		}
		current, _ := intValue(details["cached_tokens"])
		details["cached_tokens"] = current + cached
		acc["input_tokens_details"] = details
	}
	if reasoning, ok := intValue(mapValue(usage, "output_tokens_details")["reasoning_tokens"]); ok {
		details := mapValue(acc, "output_tokens_details")
		if details == nil {
			details = map[string]any{}
		}
		current, _ := intValue(details["reasoning_tokens"])
		details["reasoning_tokens"] = current + reasoning
		acc["output_tokens_details"] = details
	}
}

func summaryMap(requestID string, baseBody map[string]any, startedAt time.Time, protection, finalStatus, stoppedReason, failureReason string, rounds []map[string]any, latestReasoning any, continuationCount int, firstRound, firstTokens, firstN any, firstDecision string) map[string]any {
	if requestID == "" {
		requestID = fmt.Sprintf("codexcont-%d", startedAt.UnixNano())
	}
	endedAt := time.Now()
	model := ""
	if baseBody != nil {
		model = firstString(baseBody["model"], "")
	}
	out := map[string]any{
		"request_id":         requestID,
		"model":              model,
		"started_at":         startedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":         endedAt.UTC().Format(time.RFC3339Nano),
		"ended_at":           endedAt.UTC().Format(time.RFC3339Nano),
		"duration_ms":        endedAt.Sub(startedAt).Milliseconds(),
		"status":             finalStatus,
		"final_status":       finalStatus,
		"protection":         protection,
		"rounds":             rounds,
		"latest_round":       len(rounds),
		"continuation_count": continuationCount,
		"stopped_reason":     stoppedReason,
		"failure_reason":     failureReason,
	}
	if latestReasoning != nil {
		out["latest_reasoning_tokens"] = latestReasoning
	}
	if firstRound != nil {
		out["first_truncation_round"] = firstRound
		out["first_truncation_reasoning_tokens"] = firstTokens
		out["first_truncation_n"] = firstN
		out["first_truncation_decision"] = firstDecision
	}
	return out
}

func protectionValue(continuations int, stoppedReason, failureReason, finalStatus string) string {
	if failureReason != "" {
		return "failed"
	}
	if finalStatus == "incomplete" && stoppedReason != "" && stoppedReason != "max_continue" && stoppedReason != "no_encrypted_content" {
		return "incomplete"
	}
	if stoppedReason == "no_encrypted_content" || stoppedReason == "max_continue" || stoppedReason == "max_total_output_tokens" || stoppedReason == "tier_out_of_window" {
		if continuations > 0 {
			return "auto_continued"
		}
		return "risk_uncontinued"
	}
	if continuations > 0 {
		return "auto_continued"
	}
	return "protected_clean"
}

func findBuffer(items []bufferEntry, key string) int {
	for i := range items {
		if items[i].oi == key {
			return i
		}
	}
	return -1
}

func bufferedTypes(items []bufferEntry) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item.itemType == "" {
			out = append(out, "unknown")
			continue
		}
		out = append(out, item.itemType)
	}
	return out
}

func hasReplayableEncrypted(items []map[string]any) bool {
	if len(items) == 0 {
		return false
	}
	return firstString(items[len(items)-1]["encrypted_content"], "") != ""
}

func usageOutputTokens(usage map[string]any) int64 {
	value, _ := intValue(usage["output_tokens"])
	return value
}

func mapsToAny(items []map[string]any) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

func latestFromRounds(rounds []map[string]any) any {
	for i := len(rounds) - 1; i >= 0; i-- {
		if value, ok := rounds[i]["reasoning_tokens"]; ok {
			return value
		}
	}
	return nil
}

func oiKey(raw any) string {
	return fmt.Sprint(raw)
}

func firstString(raw any, fallback string) string {
	if text, ok := raw.(string); ok && text != "" {
		return text
	}
	return fallback
}

func marshalPayload(payload map[string]any) ([]byte, error) {
	if payload == nil {
		return nil, errors.New("payload is required")
	}
	return jsonMarshal(payload)
}

var jsonMarshal = func(v any) ([]byte, error) {
	return json.Marshal(v)
}
