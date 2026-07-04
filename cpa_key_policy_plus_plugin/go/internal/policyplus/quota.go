package policyplus

import (
	"strings"
	"time"
)

const (
	Range5H    = "5h"
	Range24H   = "24h"
	Range7D    = "7d"
	RangeMonth = "month"
)

type Window struct {
	Name string    `json:"name"`
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

func WindowFor(rangeName string, now time.Time) Window {
	now = now.UTC()
	switch strings.ToLower(strings.TrimSpace(rangeName)) {
	case Range5H:
		return Window{Name: Range5H, From: now.Add(-5 * time.Hour), To: now}
	case Range7D:
		return Window{Name: Range7D, From: now.Add(-7 * 24 * time.Hour), To: now}
	case RangeMonth:
		loc := time.FixedZone("Asia/Shanghai", 8*60*60)
		local := now.In(loc)
		start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		return Window{Name: RangeMonth, From: start.UTC(), To: now}
	default:
		return Window{Name: Range24H, From: now.Add(-24 * time.Hour), To: now}
	}
}

type QuotaDecision struct {
	Allowed  bool     `json:"allowed"`
	Reason   string   `json:"reason,omitempty"`
	UsedUSD  float64  `json:"used_usd"`
	LimitUSD *float64 `json:"limit_usd,omitempty"`
}

func CheckLimit(used float64, limit *float64) QuotaDecision {
	if limit == nil {
		return QuotaDecision{Allowed: true, UsedUSD: used}
	}
	if used >= *limit {
		return QuotaDecision{Allowed: false, Reason: "quota_exceeded", UsedUSD: used, LimitUSD: limit}
	}
	return QuotaDecision{Allowed: true, UsedUSD: used, LimitUSD: limit}
}

func ModelAllowed(allowed []string, model string) bool {
	if len(allowed) == 0 {
		return true
	}
	model = strings.TrimSpace(model)
	for _, item := range allowed {
		if strings.EqualFold(strings.TrimSpace(item), model) {
			return true
		}
	}
	return false
}
