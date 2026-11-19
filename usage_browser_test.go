package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUsageBrowserFixture(t *testing.T) {
	if os.Getenv("SIWC_USAGE_BROWSER_FIXTURE") != "1" {
		t.Skip("仅在浏览器验收时启用")
	}
	store, err := newStore(filepath.Join(usageTestDir(t), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.state.AdminKey = "browser-fixture-admin-only"
	start := time.Now().UTC().Add(-40 * 24 * time.Hour)
	if os.Getenv("SIWC_USAGE_BROWSER_RECENT") == "1" {
		start = time.Now().UTC().Truncate(time.Hour).Add(time.Second)
	}
	store.state.UsageStarted = &start
	server := newServer(newAuth(store))
	server.dashboard = newDashboard(server)
	a, b := int64(1234), int64(567)
	amount := 0.004
	if os.Getenv("SIWC_USAGE_BROWSER_RECENT") == "1" {
		server.usage.append(usageRecord{Time: time.Now().UTC(), Key: "key-a", KeyName: "测试应用 A", Model: "siwc/gpt-6.1-sol", Status: 200, Input: &a, Output: &b})
		input, output, cached, write, short, long := int64(17), int64(9), int64(12), int64(5), int64(3), int64(2)
		server.usage.append(usageRecord{Time: time.Now().UTC(), Key: "key-a", KeyName: "测试应用 A", Model: "siwc/gpt-6.2-sol", Status: 200, Input: &input, Output: &output, CachedInput: &cached, CacheWrite: &write, CacheWrite30: &short, CacheWrite12h: &long})
		if os.Getenv("SIWC_USAGE_BROWSER_LONG") == "1" {
			large, small, hit, zero := int64(300001), int64(100), int64(100000), int64(0)
			server.usage.append(usageRecord{Time: time.Now().UTC(), Key: "key-a", Model: "siwc/gpt-6.1-sol", Status: 200, Input: &large, Output: &small, CachedInput: &hit, CacheWrite: &zero})
			server.usage.append(usageRecord{Time: time.Now().UTC(), Key: "key-a", Model: "siwc/gpt-4.1", Status: 200, Input: &large, Output: &small, CachedInput: &hit})
		}
	} else {
		server.usage.append(usageRecord{Time: time.Now().UTC(), Key: "key-a", KeyName: "测试应用 A", Model: "demo/model-a", Status: 200, Input: &a, Output: &b, Amount: &amount, Currency: "USD", CostSource: "estimate"})
	}
	if os.Getenv("SIWC_USAGE_BROWSER_RECENT") == "1" {
		server.usage.append(usageRecord{Time: time.Now().UTC(), Key: "key-b", KeyName: "测试应用 B", Model: "demo/model-b", Status: 503})
	} else {
		server.usage.append(usageRecord{Time: time.Now().UTC().Add(-2 * time.Hour), Key: "key-b", KeyName: "测试应用 B", Model: "demo/model-b", Status: 200})
		for _, age := range []time.Duration{23 * time.Hour, 25 * time.Hour, 8 * 24 * time.Hour, 32 * 24 * time.Hour} {
			server.usage.append(usageRecord{Time: time.Now().UTC().Add(-age), Key: "key-a", KeyName: "测试应用 A", Model: "demo/model-a", Status: 200, Input: &a, Output: &b})
		}
	}
	done := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__usage_fixture_stop" {
			w.WriteHeader(204)
			select {
			case done <- struct{}{}:
			default:
			}
			return
		}
		server.AdminHandler().ServeHTTP(w, r)
	})
	fixture := httptest.NewServer(handler)
	defer fixture.Close()
	fmt.Printf("浏览器验收地址: %s/ui/#unlock=browser-fixture-admin-only\n", fixture.URL)
	select {
	case <-done:
	case <-time.After(5 * time.Minute):
		t.Fatal("浏览器验收超时")
	}
}
