package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func usageTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "siwc-usage-test-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestUsageWriterPreservesStreamingAndParsesSplitEvents(t *testing.T) {
	response := httptest.NewRecorder()
	response.Header().Set("Content-Type", "text/event-stream")
	w := &usageWriter{ResponseWriter: response}
	first := "data: {\"response\":{\"usage\":{\"input_tokens\":12,"
	second := "\"output_tokens\":3}}}\n\ndata: [DONE]\n\n"
	_, _ = w.Write([]byte(first))
	_, _ = w.Write([]byte(second))
	if response.Body.String() != first+second {
		t.Fatal("流式响应发生改变")
	}
	if w.input == nil || *w.input != 12 || w.output == nil || *w.output != 3 {
		t.Fatalf("用量不正确: %v %v", w.input, w.output)
	}
}

func TestUsageEndToEndPricesAndKeys(t *testing.T) {
	store, err := newStore(filepath.Join(usageTestDir(t), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	testCreateAgentKey(t, store)
	key, secret, err := store.addAgentKey("测试应用", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.upsertProvider(ExternalProvider{ID: "vendor", Name: "测试", Kind: "openai-compatible", BaseURL: "https://api.example.com", APIKey: "private-upstream", Enabled: true, ManualModels: []string{"model-a", "model-b"}}); err != nil {
		t.Fatal(err)
	}
	server := newServer(newAuth(store))
	server.dashboard = newDashboard(server)
	cookie := adminCookie(t, server)
	price := map[string]any{"model": "vendor/model-a", "price": map[string]any{"input": 2, "output": 4, "currency": "USD"}}
	if w := adminRequest(t, server.AdminHandler(), "POST", "/ui/api/usage/price", price, nil); w.Code != 401 {
		t.Fatalf("未登录可以修改单价: %d", w.Code)
	}
	if w := adminRequest(t, server.AdminHandler(), "POST", "/ui/api/usage/price", price, cookie); w.Code != 200 {
		t.Fatalf("设置单价: %d %s", w.Code, w.Body.String())
	}
	responseBody := `{"usage":{"prompt_tokens":1000000,"completion_tokens":500000},"choices":[]}`
	contentType := "application/json"
	server.router.client = &http.Client{Transport: mockRoundTrip(func(req *http.Request) (*http.Response, error) {
		return testResponse(200, contentType, responseBody), nil
	})}
	call := func(key, model string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://127.0.0.1:8087/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"secret-prompt"}]}`))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		server.APIHandler().ServeHTTP(w, req)
		return w
	}
	if w := call(secret, "model-a"); w.Code != 200 || w.Body.String() != responseBody {
		t.Fatalf("普通请求: %d %s", w.Code, w.Body.String())
	}
	price["price"] = map[string]any{"input": 10, "output": 10, "currency": "CNY"}
	if w := adminRequest(t, server.AdminHandler(), "POST", "/ui/api/usage/price", price, cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	contentType = "text/event-stream"
	responseBody = "data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"cost\":0.25,\"currency\":\"USD\"}}\n\ndata: [DONE]\n\n"
	if w := call(store.get().LocalKey, "vendor/model-a"); w.Code != 200 || w.Body.String() != responseBody {
		t.Fatalf("流式请求: %d %s", w.Code, w.Body.String())
	}
	contentType = "application/json"
	responseBody = `{"choices":[]}`
	if w := call(secret, "vendor/model-b"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	rows, err := newUsageStore(store).list()
	if err != nil || len(rows) != 3 {
		t.Fatalf("持久化: %v %v", rows, err)
	}
	if rows[0].Key != key.ID || rows[0].Model != "vendor/model-a" || rows[0].Amount == nil || *rows[0].Amount != 4 || rows[0].Currency != "USD" || rows[0].CostSource != "estimate" || rows[0].Price.Input != 2 {
		t.Fatalf("原价格估算错误: %+v", rows[0])
	}
	if rows[1].CostSource != "upstream" || rows[1].Currency != "USD" || rows[1].Amount == nil || *rows[1].Amount != 0.25 || rows[1].Key != "默认 Key" {
		t.Fatalf("上游金额错误: %+v", rows[1])
	}
	if rows[2].Input != nil || rows[2].Amount != nil {
		t.Fatalf("未知用量被当作零: %+v", rows[2])
	}
	w := adminRequest(t, server.AdminHandler(), "GET", "/ui/api/usage", nil, cookie)
	if w.Code != 200 || strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), "secret-prompt") || strings.Contains(w.Body.String(), "private-upstream") {
		t.Fatalf("统计接口泄漏或失败: %d", w.Code)
	}
	var result struct {
		Records []usageRecord         `json:"records"`
		Prices  map[string]usagePrice `json:"prices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Records) != 3 || result.Prices["vendor/model-a"].Currency != "CNY" {
		t.Fatalf("统计接口错误: %v", err)
	}
}

func TestUsageMissingCurrencyAndInterruptedStream(t *testing.T) {
	if cost, _ := usageCostFromJSON([]byte(`{"usage":{"cost":0.5}}`)); cost != nil {
		t.Fatal("缺少币种时不能认定金额")
	}
	r := httptest.NewRecorder()
	r.Header().Set("Content-Type", "text/event-stream")
	w := &usageWriter{ResponseWriter: r}
	_, _ = w.Write([]byte("data: {\"type\":\"response.failed\"}\n\ndata: [DONE]\n\n"))
	if !w.finished || !w.failed {
		t.Fatal("流式失败未被识别")
	}
}

func TestUsageSIWCResponseAndChat(t *testing.T) {
	store, err := newStore(filepath.Join(usageTestDir(t), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	testCreateAgentKey(t, store)
	if err = store.saveCredential(&Credential{AccessToken: "fake-token", Scopes: requiredScope, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	s := newServer(newAuth(store))
	s.router.mu.Lock()
	s.router.snap.ownOK = true
	s.router.snap.own = []string{"model-si"}
	s.router.mu.Unlock()
	upstream := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":17,\"input_tokens_details\":{\"cached_tokens\":12,\"cache_write_tokens\":5,\"cache_write_tokens_details\":{\"ttl_30m_tokens\":3,\"ttl_12h_tokens\":2}},\"output_tokens\":9,\"total_tokens\":26}}}\n\n"
	s.client = &http.Client{Transport: mockRoundTrip(func(req *http.Request) (*http.Response, error) {
		return testResponse(200, "text/event-stream", upstream), nil
	})}
	for _, path := range []string{"responses", "chat/completions"} {
		for _, stream := range []bool{false, true} {
			payload := `{"model":"model-si","input":"a"}`
			if path == "chat/completions" {
				payload = `{"model":"model-si","messages":[{"role":"user","content":"a"}]}`
			}
			if stream {
				payload = strings.TrimSuffix(payload, "}") + `,"stream":true}`
			}
			req := httptest.NewRequest("POST", "http://127.0.0.1:8087/v1/"+path, strings.NewReader(payload))
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("Authorization", "Bearer "+store.get().LocalKey)
			w := httptest.NewRecorder()
			s.APIHandler().ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("%s stream=%t: %d %s", path, stream, w.Code, w.Body.String())
			}
		}
	}
	rows, err := s.usage.list()
	if err != nil || len(rows) != 4 {
		t.Fatalf("记录 %v %v", rows, err)
	}
	for _, row := range rows {
		if row.Model != "siwc/model-si" || row.Input == nil || *row.Input != 17 || row.Output == nil || *row.Output != 9 || row.CachedInput == nil || *row.CachedInput != 12 || row.CacheWrite == nil || *row.CacheWrite != 5 || row.CacheWrite30 == nil || *row.CacheWrite30 != 3 || row.CacheWrite12h == nil || *row.CacheWrite12h != 2 || row.Incomplete || row.Amount != nil {
			t.Fatalf("SIWC 用量错误: %+v", row)
		}
	}
}

func TestUsageOfficialPricesAreRestrictedAndExpire(t *testing.T) {
	state := State{Providers: []ExternalProvider{{ID: "real", BaseURL: "https://api.openai.com/v1"}, {ID: "fake", BaseURL: "https://api.openai.com.example.org/v1"}}}
	prices := usageOfficialPrices(state, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC))
	price, ok := prices["siwc/gpt-6.1-sol"]
	if !ok || price.Input != 2 || price.Output != 10 || price.Cached != 0.1 || price.Currency != "USD" {
		t.Fatalf("官方单价不正确: %+v", price)
	}
	if _, ok := prices["real/gpt-6.1-sol"]; !ok {
		t.Fatal("官方来源没有价格")
	}
	if _, ok := prices["fake/gpt-6.1-sol"]; ok {
		t.Fatal("错误地将其他来源归为 OpenAI")
	}
	for model, want := range map[string]officialUsagePrice{
		"gpt-6.2-sol":   {Input: 2, Cached: 0.1, Output: 10, Currency: "USD", LongContext: true, CacheWrites: true},
		"gpt-6.2-astra": {Input: 8, Cached: 0.4, Output: 40, Currency: "USD", LongContext: true, CacheWrites: true},
	} {
		if prices["siwc/"+model] != want || prices["real/"+model] != want || prices["fake/"+model] != (officialUsagePrice{}) {
			t.Errorf("%s 官方价格或来源错误", model)
		}
	}
	if _, ok := usageOfficialPrices(state, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))["siwc/gpt-5.6-sol"]; ok {
		t.Fatal("过期促销价仍在使用")
	}
}

func TestUsageNullTokensRemainUnknown(t *testing.T) {
	a, b := usageFromJSON([]byte(`{"usage":{"prompt_tokens":null,"completion_tokens":0}}`))
	if a != nil || b == nil || *b != 0 {
		t.Fatal("空值被当成零")
	}
}

func TestUsageMultilineEvent(t *testing.T) {
	r := httptest.NewRecorder()
	r.Header().Set("Content-Type", "text/event-stream")
	w := &usageWriter{ResponseWriter: r}
	body := "data: {\"type\":\"response.completed\",\r\ndata: \"response\":{\"usage\":{\"input_tokens\":23,\"output_tokens\":11}}}\r\n\r\n"
	for _, piece := range []string{body[:13], body[13:50], body[50:]} {
		if _, err := w.Write([]byte(piece)); err != nil {
			t.Fatal(err)
		}
	}
	if r.Body.String() != body || !w.finished || w.input == nil || *w.input != 23 || w.output == nil || *w.output != 11 {
		t.Fatalf("多行事件用量或转发不正确: %+v", w)
	}
}

func TestUsageImages(t *testing.T) {
	store, err := newStore(filepath.Join(usageTestDir(t), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	testCreateAgentKey(t, store)
	if err := store.upsertProvider(ExternalProvider{ID: "image", Name: "图片", Kind: "openai-compatible", BaseURL: "https://example.com", APIKey: "key", Enabled: true, ManualModels: []string{"model"}}); err != nil {
		t.Fatal(err)
	}
	s := newServer(newAuth(store))
	body := `{"data":[],"usage":{"input_tokens":20,"output_tokens":30}}`
	s.router.client = &http.Client{Transport: mockRoundTrip(func(req *http.Request) (*http.Response, error) {
		return testResponse(200, "application/json", body), nil
	})}
	req := httptest.NewRequest("POST", "http://127.0.0.1:8087/v1/images/generations", strings.NewReader(`{"model":"image/model","prompt":"hello"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+store.get().LocalKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.APIHandler().ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != body {
		t.Fatalf("图片响应: %d %s", w.Code, w.Body.String())
	}
	rows, err := s.usage.list()
	if err != nil || len(rows) != 1 || rows[0].Model != "image/model" || rows[0].Input == nil || *rows[0].Input != 20 || rows[0].Output == nil || *rows[0].Output != 30 {
		t.Fatalf("图片统计: %v %v", rows, err)
	}
}

func TestUsageLargeImageResponseStaysStreaming(t *testing.T) {
	response := httptest.NewRecorder()
	response.Header().Set("Content-Type", "application/json")
	w := &usageWriter{ResponseWriter: response}
	body := `{"data":[{"b64_json":"` + strings.Repeat("A", 2<<20) + `"}],"usage":{"input_tokens":30,"output_tokens":50}}`
	for pos := 0; pos < len(body); pos += 8192 {
		end := pos + 8192
		if end > len(body) {
			end = len(body)
		}
		if _, err := w.Write([]byte(body[pos:end])); err != nil {
			t.Fatal(err)
		}
	}
	a, b := usageFromJSON(w.buf)
	if response.Body.String() != body || len(w.buf) > 1024 || a == nil || *a != 30 || b == nil || *b != 50 {
		t.Fatalf("大图响应或用量错误: 缓存=%d 输入=%v 输出=%v", len(w.buf), a, b)
	}
	bad := &usageWriter{ResponseWriter: httptest.NewRecorder()}
	_, _ = bad.Write([]byte(`{"b64_json":"invalid\\z","usage":{"input_tokens":1,"output_tokens":2}}`))
	if !bad.overflow || len(bad.buf) != 0 {
		t.Fatal("异常图像编码不应产生估算")
	}
}

func TestUsageVideoIsAttributedOnceAcrossRestart(t *testing.T) {
	store, err := newStore(filepath.Join(usageTestDir(t), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	testCreateAgentKey(t, store)
	key, secret, err := store.addAgentKey("视频应用", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.upsertProvider(ExternalProvider{ID: "ark", Name: "视频", Kind: "ark-video", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", APIKey: "fake-key", Enabled: true, ManualModels: []string{"video"}}); err != nil {
		t.Fatal(err)
	}
	store.state.UsagePrices = map[string]usagePrice{"ark/video": {Input: 1, Output: 2, Currency: "CNY"}}
	server := newServer(newAuth(store))
	status := `{"id":"task-1","status":"succeeded","usage":{"completion_tokens":500000,"total_tokens":500000}}`
	transport := mockRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Method == "POST" {
			return testResponse(200, "application/json", `{"id":"task-1"}`), nil
		}
		return testResponse(200, "application/json", status), nil
	})
	server.router.client = &http.Client{Transport: transport}
	call := func(s *Server, method, path, body, key string) {
		req := httptest.NewRequest(method, "http://127.0.0.1:8087"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.APIHandler().ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("视频请求失败: %d %s", w.Code, w.Body.String())
		}
	}
	call(server, "POST", "/v1/contents/generations/tasks", `{"model":"ark/video","content":[]}`, secret)
	rows, err := server.usage.list()
	if err != nil || len(rows) != 1 || rows[0].TaskState != "pending" || rows[0].Input != nil {
		t.Fatalf("未完成任务: %+v %v", rows, err)
	}
	status = `{"id":"wrong-task","status":"succeeded","usage":{"completion_tokens":500000,"total_tokens":500000}}`
	call(server, "GET", "/v1/contents/generations/tasks/task-1?provider=ark", "", store.get().LocalKey)
	rows, _ = server.usage.list()
	if rows[0].TaskState != "pending" {
		t.Fatal("错误任务 ID 不应结算")
	}
	status = `{"id":"task-1","status":"succeeded","usage":{"completion_tokens":500000,"total_tokens":500000}}`
	store.state.UsagePrices["ark/video"] = usagePrice{Output: 99, Currency: "USD"}
	call(server, "GET", "/v1/contents/generations/tasks/task-1?provider=ark", "", store.get().LocalKey)
	call(server, "GET", "/v1/contents/generations/tasks/task-1?provider=ark", "", secret)
	restarted := newServer(newAuth(store))
	restarted.router.client = &http.Client{Transport: transport}
	call(restarted, "GET", "/v1/contents/generations/tasks/task-1?provider=ark", "", secret)
	rows, err = restarted.usage.list()
	if err != nil || len(rows) != 1 {
		t.Fatalf("任务重复统计: %+v %v", rows, err)
	}
	row := rows[0]
	if row.Key != key.ID || row.Model != "ark/video" || row.TaskState != "succeeded" || row.Input == nil || *row.Input != 0 || row.Output == nil || *row.Output != 500000 || row.Amount == nil || *row.Amount != 1 || row.Currency != "CNY" {
		t.Fatalf("视频归属或估算错误: %+v", row)
	}
}

func TestUsageVideoConcurrentSettlementAndDuplicateSubmission(t *testing.T) {
	u := &usageStore{path: filepath.Join(usageTestDir(t), "usage.jsonl")}
	ref := usageTaskRef("ark", "one")
	record := usageRecord{Time: time.Now().UTC(), Key: "first", Model: "ark/video", TaskRef: ref, TaskState: "pending"}
	u.appendVideo(record)
	record.Key = "second"
	u.appendVideo(record)
	body := []byte(`{"id":"one","status":"succeeded","usage":{"completion_tokens":10,"total_tokens":10}}`)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); u.completeVideo(ref, "one", body, true) }()
	}
	wg.Wait()
	contents, err := os.ReadFile(u.path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(contents), "\n"); n != 2 {
		t.Fatalf("日志写入 %d 条，预期创建和结算各一条", n)
	}
	rows, err := u.list()
	if err != nil || len(rows) != 1 || rows[0].Key != "first" || rows[0].Output == nil || *rows[0].Output != 10 {
		t.Fatalf("并发去重失败: %+v %v", rows, err)
	}
}

func TestUsageCoveragePersistsOnRestart(t *testing.T) {
	store, err := newStore(filepath.Join(usageTestDir(t), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = newUsageStore(store)
	start := store.get().UsageStarted
	if start == nil {
		t.Fatal("没有记录开始时间")
	}
	reloaded, err := newStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	_ = newUsageStore(reloaded)
	if reloaded.get().UsageStarted == nil || !reloaded.get().UsageStarted.Equal(*start) {
		t.Fatal("重启后覆盖起点发生改变")
	}
}

func TestUsageStorePersistenceAndUnknown(t *testing.T) {
	path := filepath.Join(usageTestDir(t), "usage.jsonl")
	u := &usageStore{path: path}
	u.append(usageRecord{Time: time.Now().UTC(), Key: "key1", Model: "m", Status: 200})
	rows, err := (&usageStore{path: path}).list()
	if err != nil || len(rows) != 1 || rows[0].Input != nil || rows[0].Output != nil {
		t.Fatalf("用量记录错误: %v %v", rows, err)
	}
}
