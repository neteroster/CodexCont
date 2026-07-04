package policyplus

import "strings"

const perMillion = 1_000_000.0

type TokenUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

type CostBreakdown struct {
	Source string             `json:"source"`
	Model  string             `json:"model"`
	Prices ModelPrice         `json:"prices"`
	Tokens map[string]int64   `json:"tokens"`
	Costs  map[string]float64 `json:"costs"`
}

func PriceForModel(prices map[string]ModelPrice, model string) (ModelPrice, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return ModelPrice{}, false
	}
	if price, ok := prices[model]; ok {
		return price, true
	}
	lower := strings.ToLower(model)
	for name, price := range prices {
		if strings.ToLower(name) == lower {
			return price, true
		}
	}
	return ModelPrice{}, false
}

func CostForUsage(price ModelPrice, usage TokenUsage, model string) CostBreakdown {
	input := max64(usage.InputTokens, 0)
	output := max64(usage.OutputTokens, 0)
	cached := max64(usage.CachedTokens, 0)
	cacheRead := max64(usage.CacheReadTokens, 0)
	cacheCreation := max64(usage.CacheCreationTokens, 0)
	reasoning := max64(usage.ReasoningTokens, 0)
	billableInput := max64(input-cached, 0)
	cacheReadPrice := price.CacheReadPerMillion
	if cacheReadPrice <= 0 {
		cacheReadPrice = price.InputPerMillion
	}
	cacheCreationPrice := price.CacheCreationPerMillion
	if cacheCreationPrice <= 0 {
		cacheCreationPrice = price.InputPerMillion
	}
	inputCost := float64(billableInput) * price.InputPerMillion / perMillion
	cachedCost := float64(cached) * cacheReadPrice / perMillion
	cacheReadCost := float64(cacheRead) * cacheReadPrice / perMillion
	cacheCreationCost := float64(cacheCreation) * cacheCreationPrice / perMillion
	outputCost := float64(output) * price.OutputPerMillion / perMillion
	total := inputCost + cachedCost + cacheReadCost + cacheCreationCost + outputCost
	totalTokens := usage.TotalTokens
	if totalTokens <= 0 {
		totalTokens = input + output
	}
	if strings.TrimSpace(model) == "" {
		model = price.Model
	}
	return CostBreakdown{
		Source: "key_policy_plus_price_book",
		Model:  model,
		Prices: price,
		Tokens: map[string]int64{
			"input":                   input,
			"billable_uncached_input": billableInput,
			"cached_input":            cached,
			"cache_read":              cacheRead,
			"cache_creation":          cacheCreation,
			"output":                  output,
			"reasoning":               reasoning,
			"visible_output_estimate": max64(output-reasoning, 0),
			"total":                   totalTokens,
		},
		Costs: map[string]float64{
			"input":          inputCost,
			"cached_input":   cachedCost,
			"cache_read":     cacheReadCost,
			"cache_creation": cacheCreationCost,
			"output":         outputCost,
			"total":          total,
		},
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
