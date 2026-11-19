package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type usageRecord struct {
	Time          time.Time   `json:"time"`
	Key           string      `json:"key"`
	KeyName       string      `json:"key_name,omitempty"`
	Model         string      `json:"model"`
	Status        int         `json:"status"`
	Input         *int64      `json:"input"`
	Output        *int64      `json:"output"`
	CachedInput   *int64      `json:"cached_input,omitempty"`
	CacheWrite    *int64      `json:"cache_write,omitempty"`
	CacheWrite30  *int64      `json:"cache_write_30,omitempty"`
	CacheWrite12h *int64      `json:"cache_write_12h,omitempty"`
	Incomplete    bool        `json:"incomplete,omitempty"`
	Amount        *float64    `json:"amount,omitempty"`
	Currency      string      `json:"currency,omitempty"`
	CostSource    string      `json:"cost_source,omitempty"`
	Price         *usagePrice `json:"price,omitempty"`
	TaskRef       string      `json:"task_ref,omitempty"`
	TaskState     string      `json:"task_state,omitempty"`
}

type usagePrice struct {
	Input    float64 `json:"input"`
	Output   float64 `json:"output"`
	Currency string  `json:"currency"`
}

type usageStore struct {
	mu        sync.Mutex
	path      string
	lastError error
	tasks     map[string]usageRecord
}

func (u *usageStore) append(record usageRecord) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.appendLocked(record)
}

func (u *usageStore) appendLocked(record usageRecord) {
	b, err := json.Marshal(record)
	if err == nil {
		var f *os.File
		f, err = os.OpenFile(u.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			_, err = f.Write(append(b, '\n'))
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		u.lastError = err
	} else if record.TaskRef != "" && u.tasks != nil {
		u.tasks[record.TaskRef] = record
	}
}

func (u *usageStore) list() ([]usageRecord, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.lastError != nil {
		return nil, fmt.Errorf("用量写入失败: %w", u.lastError)
	}
	f, err := os.Open(u.path)
	if os.IsNotExist(err) {
		return []usageRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var records []usageRecord
	tasks := map[string]int{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		var rec usageRecord
		if err := json.Unmarshal(s.Bytes(), &rec); err != nil {
			return nil, err
		}
		if rec.Time.After(time.Now().Add(-31 * 24 * time.Hour)) {
			if index, ok := tasks[rec.TaskRef]; rec.TaskRef != "" && ok {
				records[index] = rec
			} else {
				if rec.TaskRef != "" {
					tasks[rec.TaskRef] = len(records)
				}
				records = append(records, rec)
			}
		}
	}
	return records, s.Err()
}

func (u *usageStore) loadTasksLocked() error {
	if u.tasks != nil {
		return nil
	}
	tasks := map[string]usageRecord{}
	f, err := os.Open(u.path)
	if os.IsNotExist(err) {
		u.tasks = tasks
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		var record usageRecord
		if err := json.Unmarshal(s.Bytes(), &record); err != nil {
			return err
		}
		if record.TaskRef != "" {
			tasks[record.TaskRef] = record
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	u.tasks = tasks
	return nil
}

func usageTaskRef(provider, id string) string {
	hash := sha256.Sum256([]byte(provider + "\x00" + id))
	return hex.EncodeToString(hash[:])
}

func (u *usageStore) appendVideo(record usageRecord) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if record.TaskRef != "" {
		if err := u.loadTasksLocked(); err != nil {
			u.lastError = err
			return
		}
		if _, exists := u.tasks[record.TaskRef]; exists {
			return
		}
	}
	u.appendLocked(record)
}

func (u *usageStore) completeVideo(ref, id string, body []byte, official bool) {
	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Usage  struct {
			Completion *int64 `json:"completion_tokens"`
			Total      *int64 `json:"total_tokens"`
			Input      *int64 `json:"input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &result) != nil || result.ID != id || result.Status != "succeeded" && result.Status != "failed" && result.Status != "cancelled" && result.Status != "expired" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.loadTasksLocked(); err != nil {
		u.lastError = err
		return
	}
	record, ok := u.tasks[ref]
	if !ok || record.TaskState != "pending" {
		return
	}
	input, output := usageFromJSON(body)
	if input == nil && official && result.Usage.Input == nil && result.Usage.Completion != nil && result.Usage.Total != nil && *result.Usage.Completion >= 0 && *result.Usage.Completion == *result.Usage.Total {
		zero := int64(0)
		input, output = &zero, result.Usage.Completion
	}
	if result.Status == "succeeded" && (input == nil || output == nil) {
		return
	}
	record.Input, record.Output = input, output
	record.Incomplete = result.Status != "succeeded"
	record.TaskState = result.Status
	if amount, currency := usageCostFromJSON(body); amount != nil {
		record.Amount = amount
		record.Currency = currency
		record.CostSource = "upstream"
	} else if record.Price != nil && input != nil && output != nil {
		amount := (float64(*input)*record.Price.Input + float64(*output)*record.Price.Output) / 1e6
		record.Amount = &amount
		record.Currency = record.Price.Currency
		record.CostSource = "estimate"
	}
	u.appendLocked(record)
}

func usageFromJSON(data []byte) (*int64, *int64) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil, nil
	}
	if nested, ok := obj["response"]; ok {
		a, b := usageFromJSON(nested)
		if a != nil && b != nil {
			return a, b
		}
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(obj["usage"], &usage) != nil {
		return nil, nil
	}
	read := func(keys ...string) *int64 {
		for _, key := range keys {
			var n int64
			if v, ok := usage[key]; ok && string(v) != "null" && json.Unmarshal(v, &n) == nil && n >= 0 {
				return &n
			}
		}
		return nil
	}
	return read("input_tokens", "prompt_tokens"), read("output_tokens", "completion_tokens")
}

type usageWriter struct {
	http.ResponseWriter
	status                                  int
	buf                                     []byte
	overflow                                bool
	eventData                               []byte
	eventOverflow                           bool
	jsonData                                usageJSONCapture
	input, output                           *int64
	cachedInput                             *int64
	cacheWrite, cacheWrite30, cacheWrite12h *int64
	amount                                  *float64
	currency                                string
	stream, finished, failed                bool
}

func (w *usageWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *usageWriter) Write(p []byte) (int, error) {
	original := p
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		w.stream = true
		for len(p) > 0 {
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				i = len(p)
			}
			if !w.overflow && len(w.buf)+i <= 1<<20 {
				w.buf = append(w.buf, p[:i]...)
			} else {
				w.overflow = true
			}
			if i == len(p) {
				break
			}
			if w.overflow {
				w.eventOverflow = true
			} else {
				w.consumeSSELine(bytes.TrimSuffix(w.buf, []byte("\r")))
			}
			w.buf = nil
			w.overflow = false
			p = p[i+1:]
		}
	} else {
		w.jsonData.write(p)
		w.buf = w.jsonData.buf
		w.overflow = w.jsonData.tooLarge
	}
	n, err := w.ResponseWriter.Write(original)
	if err != nil || n != len(original) {
		w.failed = true
	}
	return n, err
}

func (w *usageWriter) consumeSSELine(line []byte) {
	if len(line) == 0 {
		if !w.eventOverflow {
			w.captureEvent(bytes.TrimSpace(w.eventData))
		}
		w.eventData = nil
		w.eventOverflow = false
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) || w.eventOverflow {
		return
	}
	data := line[5:]
	if len(data) > 0 && data[0] == ' ' {
		data = data[1:]
	}
	if len(w.eventData)+len(data)+1 > 1<<20 {
		w.eventData = nil
		w.eventOverflow = true
		return
	}
	if len(w.eventData) > 0 {
		w.eventData = append(w.eventData, '\n')
	}
	w.eventData = append(w.eventData, data...)
}

func (w *usageWriter) captureEvent(data []byte) {
	if bytes.Equal(data, []byte("[DONE]")) {
		w.finished = true
	}
	var event struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &event) == nil {
		if event.Type == "response.completed" {
			w.finished = true
		}
		if event.Type == "response.failed" || event.Type == "response.incomplete" || len(event.Error) > 0 && string(event.Error) != "null" {
			w.failed = true
		}
	}
	a, b := usageFromJSON(data)
	if a != nil && b != nil {
		w.input, w.output = a, b
	}
	if cached := usageCachedFromJSON(data); cached != nil {
		w.cachedInput = cached
	}
	if total, short, long := usageCacheWritesFromJSON(data); total != nil {
		w.cacheWrite, w.cacheWrite30, w.cacheWrite12h = total, short, long
	}
	if amount, currency := usageCostFromJSON(data); amount != nil {
		w.amount, w.currency = amount, currency
	}
}
func (w *usageWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) usageKey(secret string) (string, string) {
	s.auth.store.mu.Lock()
	defer s.auth.store.mu.Unlock()
	if secret == s.auth.store.state.LocalKey {
		return "默认 Key", "默认 Key"
	}
	hash := sha256.Sum256([]byte(secret))
	encoded := hex.EncodeToString(hash[:])
	for _, key := range s.auth.store.state.AgentKeys {
		if subtle.ConstantTimeCompare([]byte(key.Hash), []byte(encoded)) == 1 {
			return key.ID, key.Name
		}
	}
	return "未知 Key", "未知 Key"
}

func (s *Server) observeUsage(w http.ResponseWriter, req *http.Request, model string) (http.ResponseWriter, func()) {
	if s.usage == nil {
		return w, func() {}
	}
	observer := &usageWriter{ResponseWriter: w}
	key, name := s.usageKey(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
	started := time.Now().UTC()
	price, hasPrice := s.auth.store.get().UsagePrices[model]
	return observer, func() {
		if observer.input == nil || observer.output == nil {
			observer.input, observer.output = usageFromJSON(observer.buf)
		}
		if observer.cachedInput == nil {
			observer.cachedInput = usageCachedFromJSON(observer.buf)
		}
		if observer.cacheWrite == nil {
			observer.cacheWrite, observer.cacheWrite30, observer.cacheWrite12h = usageCacheWritesFromJSON(observer.buf)
		}
		if observer.amount == nil {
			observer.amount, observer.currency = usageCostFromJSON(observer.buf)
		}
		status := observer.status
		if status == 0 {
			status = http.StatusOK
		}
		record := usageRecord{Time: started, Key: key, KeyName: name, Model: model, Status: status, Input: observer.input, Output: observer.output, CachedInput: observer.cachedInput, CacheWrite: observer.cacheWrite, CacheWrite30: observer.cacheWrite30, CacheWrite12h: observer.cacheWrite12h, Incomplete: observer.failed || observer.stream && !observer.finished || req.Context().Err() != nil}
		if observer.amount != nil {
			record.Amount = observer.amount
			record.Currency = observer.currency
			record.CostSource = "upstream"
		} else if hasPrice && record.Input != nil && record.Output != nil {
			amount := (float64(*record.Input)*price.Input + float64(*record.Output)*price.Output) / 1e6
			record.Amount = &amount
			record.Currency = price.Currency
			record.CostSource = "estimate"
			record.Price = &price
		}
		s.usage.append(record)
	}
}

func usageCachedFromJSON(data []byte) *int64 {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil
	}
	if nested, ok := obj["response"]; ok {
		if v := usageCachedFromJSON(nested); v != nil {
			return v
		}
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(obj["usage"], &usage) != nil {
		return nil
	}
	for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
		var details map[string]json.RawMessage
		if json.Unmarshal(usage[key], &details) != nil {
			continue
		}
		var n int64
		raw, ok := details["cached_tokens"]
		if ok && string(raw) != "null" && json.Unmarshal(raw, &n) == nil && n >= 0 {
			return &n
		}
	}
	return nil
}

func usageCacheWritesFromJSON(data []byte) (*int64, *int64, *int64) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil, nil, nil
	}
	if nested, ok := obj["response"]; ok {
		if a, b, c := usageCacheWritesFromJSON(nested); a != nil {
			return a, b, c
		}
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(obj["usage"], &usage) != nil {
		return nil, nil, nil
	}
	for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
		var details map[string]json.RawMessage
		if json.Unmarshal(usage[key], &details) != nil {
			continue
		}
		read := func(raw json.RawMessage) *int64 {
			var n int64
			if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &n) != nil || n < 0 {
				return nil
			}
			return &n
		}
		total := read(details["cache_write_tokens"])
		if total == nil {
			continue
		}
		var split map[string]json.RawMessage
		_ = json.Unmarshal(details["cache_write_tokens_details"], &split)
		short, long := read(split["ttl_30m_tokens"]), read(split["ttl_12h_tokens"])
		if short == nil || long == nil || *short > *total || *long != *total-*short {
			short, long = nil, nil
		}
		return total, short, long
	}
	return nil, nil, nil
}

func (s *Server) observeVideoSubmission(w http.ResponseWriter, req *http.Request, model, provider string, official bool) (http.ResponseWriter, func()) {
	if s.usage == nil {
		return w, func() {}
	}
	observer := &usageWriter{ResponseWriter: w}
	key, name := s.usageKey(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
	started := time.Now().UTC()
	price, hasPrice := s.auth.store.get().UsagePrices[model]
	return observer, func() {
		status := observer.status
		if status == 0 {
			status = http.StatusOK
		}
		record := usageRecord{Time: started, Key: key, KeyName: name, Model: model, Status: status, Incomplete: observer.failed || req.Context().Err() != nil}
		var result struct {
			ID string `json:"id"`
		}
		if status >= 200 && status < 300 && json.Unmarshal(observer.buf, &result) == nil && result.ID != "" && len(result.ID) <= 180 && !strings.Contains(result.ID, "/") && !strings.Contains(result.ID, "..") {
			record.TaskRef = usageTaskRef(provider, result.ID)
			record.TaskState = "pending"
			if hasPrice {
				record.Price = &price
			}
		} else {
			record.Incomplete = true
		}
		s.usage.appendVideo(record)
		if record.TaskRef != "" {
			s.usage.completeVideo(record.TaskRef, result.ID, observer.buf, official)
		}
	}
}
func newUsageStore(s *Store) *usageStore {
	u := &usageStore{path: filepath.Join(filepath.Dir(s.path), "usage.jsonl")}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.UsageStarted == nil {
		started := time.Now().UTC()
		s.state.UsageStarted = &started
		if err := s.saveLocked(); err != nil {
			s.state.UsageStarted = nil
			u.lastError = err
		}
	}
	return u
}

func (d *Dashboard) usageHandler(w http.ResponseWriter, r *http.Request) {
	records, err := d.server.usage.list()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "usage_unavailable", "读取用量记录失败")
		return
	}
	keys := map[string]string{"默认 Key": "默认 Key", "未知 Key": "未知 Key"}
	for _, item := range d.server.auth.store.get().AgentKeys {
		keys[item.ID] = item.Name
	}
	state := d.server.auth.store.get()
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "keys": keys, "prices": state.UsagePrices, "official_prices": usageOfficialPrices(state, time.Now()), "official_prices_checked_at": "2026-11-18", "started_at": state.UsageStarted})
}

func usageCostFromJSON(data []byte) (*float64, string) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil, ""
	}
	if nested, ok := obj["response"]; ok {
		if n, c := usageCostFromJSON(nested); n != nil {
			return n, c
		}
	}
	var usage struct {
		Cost     *float64 `json:"cost"`
		Currency string   `json:"currency"`
	}
	if json.Unmarshal(obj["usage"], &usage) != nil || usage.Cost == nil || *usage.Cost < 0 || math.IsNaN(*usage.Cost) || math.IsInf(*usage.Cost, 0) || !validCurrency(usage.Currency) {
		return nil, ""
	}
	return usage.Cost, usage.Currency
}

func validCurrency(c string) bool {
	return len(c) == 3 && c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z' && c[2] >= 'A' && c[2] <= 'Z'
}

func (d *Dashboard) usagePriceHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string      `json:"model"`
		Price *usagePrice `json:"price"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || strings.TrimSpace(req.Model) != req.Model || len(req.Model) == 0 || len(req.Model) > 256 {
		writeError(w, 400, "invalid_price", "模型或价格无效")
		return
	}
	if p := req.Price; p != nil && (!validCurrency(p.Currency) || math.IsNaN(p.Input) || math.IsNaN(p.Output) || math.IsInf(p.Input, 0) || math.IsInf(p.Output, 0) || p.Input < 0 || p.Output < 0 || p.Input > 1e9 || p.Output > 1e9) {
		writeError(w, 400, "invalid_price", "价格必须为非负数，币种须为三位大写字母")
		return
	}
	s := d.server.auth.store
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.state.UsagePrices
	next := make(map[string]usagePrice, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	if req.Price == nil {
		delete(next, req.Model)
	} else {
		next[req.Model] = *req.Price
	}
	s.state.UsagePrices = next
	if err := s.saveLocked(); err != nil {
		s.state.UsagePrices = old
		writeError(w, 500, "save_failed", "无法保存价格")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
