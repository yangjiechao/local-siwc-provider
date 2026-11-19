package main

import (
	"net/url"
	"strings"
	"time"
)

// 官方标准文本价格快照；未列出或需按模态细分的模型保持未知。
type officialUsagePrice struct {
	Input       float64 `json:"input"`
	Cached      float64 `json:"cached"`
	Output      float64 `json:"output"`
	Currency    string  `json:"currency"`
	LongContext bool    `json:"long_context"`
	CacheWrites bool    `json:"cache_writes"`
}

var officialOpenAIPrices = map[string]officialUsagePrice{
	"gpt-5.6-luna":  {Input: 0.2, Cached: 0.02, Output: 1.2, Currency: "USD", LongContext: true},
	"gpt-5.6-terra": {Input: 2, Cached: 0.2, Output: 12, Currency: "USD", LongContext: true},
	"gpt-5.6-sol":   {Input: 4, Cached: 0.4, Output: 20, Currency: "USD", LongContext: true},
	"gpt-6-luna":    {Input: 0.1, Cached: 0.01, Output: 0.5, Currency: "USD", LongContext: true},
	"gpt-6-sol":     {Input: 2, Cached: 0.2, Output: 10, Currency: "USD", LongContext: true},
	"gpt-6-astra":   {Input: 10, Cached: 1, Output: 50, Currency: "USD", LongContext: true},
	"gpt-6.1-sol":   {Input: 2, Cached: 0.1, Output: 10, Currency: "USD", LongContext: true},
	"gpt-6.1-terra": {Input: 1, Cached: 0.05, Output: 5, Currency: "USD", LongContext: true},
	"gpt-6.2-sol":   {Input: 2, Cached: 0.1, Output: 10, Currency: "USD", LongContext: true},
	"gpt-6.2-astra": {Input: 8, Cached: 0.4, Output: 40, Currency: "USD", LongContext: true},
	"gpt-4.1":       {Input: 2, Cached: 0.5, Output: 8, Currency: "USD"},
	"gpt-4.1-mini":  {Input: 0.4, Cached: 0.1, Output: 1.6, Currency: "USD"},
	"gpt-4o":        {Input: 2.5, Cached: 1.25, Output: 10, Currency: "USD"},
	"gpt-4o-mini":   {Input: 0.15, Cached: 0.075, Output: 0.6, Currency: "USD"},
	"gpt-5":         {Input: 1.25, Cached: 0.125, Output: 10, Currency: "USD"},
	"gpt-5-mini":    {Input: 0.25, Cached: 0.025, Output: 2, Currency: "USD"},
	"gpt-5-nano":    {Input: 0.05, Cached: 0.005, Output: 0.4, Currency: "USD"},
	"gpt-5-pro":     {Input: 15, Cached: 15, Output: 120, Currency: "USD"},
	"gpt-5.1":       {Input: 1.25, Cached: 0.125, Output: 10, Currency: "USD"},
	"gpt-5.2":       {Input: 1.75, Cached: 0.175, Output: 14, Currency: "USD"},
	"gpt-5.2-pro":   {Input: 21, Cached: 21, Output: 168, Currency: "USD"},
	"gpt-5.3-codex": {Input: 1.75, Cached: 0.175, Output: 14, Currency: "USD"},
	"gpt-5.4":       {Input: 2.5, Cached: 0.25, Output: 15, Currency: "USD", LongContext: true},
	"gpt-5.4-mini":  {Input: 0.75, Cached: 0.075, Output: 4.5, Currency: "USD"},
	"gpt-5.4-nano":  {Input: 0.2, Cached: 0.02, Output: 1.25, Currency: "USD"},
	"gpt-5.4-pro":   {Input: 30, Cached: 30, Output: 180, Currency: "USD", LongContext: true},
	"gpt-5.5":       {Input: 5, Cached: 0.5, Output: 30, Currency: "USD", LongContext: true},
	"gpt-5.5-pro":   {Input: 30, Cached: 30, Output: 180, Currency: "USD"},
	"o3":            {Input: 2, Cached: 0.5, Output: 8, Currency: "USD"},
}

var officialOpenAIAliases = map[string]string{
	"gpt-4.1-2025-04-14": "gpt-4.1", "gpt-4.1-mini-2025-04-14": "gpt-4.1-mini",
	"gpt-4o-2024-08-06": "gpt-4o", "gpt-4o-2024-11-20": "gpt-4o", "gpt-4o-mini-2024-07-18": "gpt-4o-mini",
	"gpt-5-2025-08-07": "gpt-5", "gpt-5-mini-2025-08-07": "gpt-5-mini", "gpt-5-nano-2025-08-07": "gpt-5-nano", "gpt-5-pro-2025-10-06": "gpt-5-pro",
	"gpt-5.1-2025-11-13": "gpt-5.1", "gpt-5.2-2025-12-11": "gpt-5.2", "gpt-5.2-pro-2025-12-11": "gpt-5.2-pro",
	"gpt-5.4-2026-03-05": "gpt-5.4", "gpt-5.4-mini-2026-03-17": "gpt-5.4-mini", "gpt-5.4-nano-2026-03-17": "gpt-5.4-nano", "gpt-5.4-pro-2026-03-05": "gpt-5.4-pro",
	"gpt-5.5-2026-04-23": "gpt-5.5", "gpt-5.5-pro-2026-04-23": "gpt-5.5-pro", "o3-2025-04-16": "o3",
}

func usageOfficialPrices(state State, now time.Time) map[string]officialUsagePrice {
	result := map[string]officialUsagePrice{}
	for model, price := range officialOpenAIPrices {
		price.CacheWrites = strings.HasPrefix(model, "gpt-5.6-") || strings.HasPrefix(model, "gpt-6")
		// 此促销价在官方公布的期限后不能继续作为当前价。
		if model == "gpt-5.6-sol" && !now.Before(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
			continue
		}
		result["siwc/"+model] = price
		for _, provider := range state.Providers {
			u, err := url.Parse(provider.BaseURL)
			if err == nil && u.Scheme == "https" && u.Hostname() == "api.openai.com" {
				result[provider.ID+"/"+model] = price
			}
		}
	}
	for alias, base := range officialOpenAIAliases {
		if price, ok := officialOpenAIPrices[base]; ok {
			result["siwc/"+alias] = price
			for _, provider := range state.Providers {
				u, err := url.Parse(provider.BaseURL)
				if err == nil && u.Scheme == "https" && u.Hostname() == "api.openai.com" {
					result[provider.ID+"/"+alias] = price
				}
			}
		}
	}
	return result
}
