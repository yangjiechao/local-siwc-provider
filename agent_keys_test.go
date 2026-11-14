package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationKeys(t *testing.T) {
	dir, err := os.MkdirTemp("", "siwc-agent-keys-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	store, err := newStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.get().LocalKey != "" || store.validAgentKey("") {
		t.Fatal("新安装不应预置 Key 或接受空 Key")
	}
	if err := store.saveLocalKey("legacy-key-1234567890"); err != nil {
		t.Fatal(err)
	}
	svc := newServer(newAuth(store))
	svc.dashboard = newDashboard(svc)
	h := svc.Handler()
	if w := dashboardRequest(t, h, "POST", "/ui/api/agent-keys", map[string]string{"name": "应用甲"}, nil, true, ""); w.Code != 401 {
		t.Fatalf("未授权创建：%d", w.Code)
	}
	w := dashboardRequest(t, h, "POST", "/ui/api/session", map[string]string{"key": store.get().AdminKey}, nil, true, "http://127.0.0.1:8787")
	if w.Code != 200 {
		t.Fatalf("控制台解锁：%d", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if w := dashboardRequest(t, h, "POST", "/ui/api/agent-keys", map[string]string{"name": "应用甲"}, cookie, false, ""); w.Code != 403 {
		t.Fatalf("缺少防跨站请求头：%d", w.Code)
	}
	if w := dashboardRequest(t, h, "POST", "/ui/api/agent-keys", map[string]string{"name": "应用甲"}, cookie, true, "https://evil.example"); w.Code != 403 {
		t.Fatalf("错误来源：%d", w.Code)
	}
	create := func(name, key string) (string, string) {
		t.Helper()
		w := dashboardRequest(t, h, "POST", "/ui/api/agent-keys", map[string]string{"name": name, "key": key}, cookie, true, "http://127.0.0.1:8787")
		if w.Code != 200 {
			t.Fatalf("创建 Key：%d", w.Code)
		}
		var result map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result["id"], result["key"]
	}
	id1, key1 := create("应用甲", "")
	customKey := "custom-example-key-123456789"
	id2, key2 := create("应用乙", customKey)
	if key2 != customKey {
		t.Fatal("未保存指定 Key")
	}
	for _, badKey := range []string{"short", "contains spaces in key", "含有中文的字符key", key1, key2, store.get().LocalKey} {
		w := dashboardRequest(t, h, "POST", "/ui/api/agent-keys", map[string]string{"name": "应用丙", "key": badKey}, cookie, true, "http://127.0.0.1:8787")
		if w.Code != 400 {
			t.Fatalf("错误或重复 Key 未被拒绝：%d", w.Code)
		}
	}
	w = dashboardRequest(t, h, "POST", "/ui/api/key", map[string]string{"key": key2}, cookie, true, "http://127.0.0.1:8787")
	if w.Code != 400 || store.get().LocalKey == key2 {
		t.Fatal("默认 Key 不应与独立 Key 相同")
	}
	if key1 == key2 || !store.validAgentKey(store.get().LocalKey) || !store.validAgentKey(key1) || !store.validAgentKey(key2) {
		t.Fatal("独立 Key 认证失败")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), key1) || !strings.Contains(string(data), key2) {
		t.Fatal("Key 未保存以供显示和复制")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("凭证文件权限不正确")
	}
	w = dashboardRequest(t, h, "GET", "/ui/api/agent-keys", nil, cookie, false, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), key1) || strings.Contains(w.Body.String(), "hash") {
		t.Fatal("列表暴露 Key 或摘要")
	}
	w = dashboardRequest(t, h, "POST", "/ui/api/agent-keys/reveal", map[string]string{"id": id1}, cookie, true, "http://127.0.0.1:8787")
	if w.Code != 200 || !strings.Contains(w.Body.String(), key1) {
		t.Fatal("新 Key 无法显示")
	}
	store.mu.Lock()
	store.state.AgentKeys[0].Secret = ""
	if err := store.saveLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()
	w = dashboardRequest(t, h, "POST", "/ui/api/agent-keys/reveal", map[string]string{"id": id1}, cookie, true, "http://127.0.0.1:8787")
	if w.Code != 400 || !store.validAgentKey(key1) {
		t.Fatal("历史摘要 Key 状态错误")
	}
	w = dashboardRequest(t, h, "POST", "/ui/api/agent-keys/update", map[string]string{"id": id1, "key": key1}, cookie, true, "http://127.0.0.1:8787")
	if w.Code != 200 || !store.validAgentKey(key1) {
		t.Fatal("填写原 Key 不能恢复显示")
	}
	w = dashboardRequest(t, h, "POST", "/ui/api/agent-keys/update", map[string]string{"id": id1, "key": key2}, cookie, true, "http://127.0.0.1:8787")
	if w.Code != 400 || !store.validAgentKey(key1) {
		t.Fatal("修改成重复 Key 应被拒绝")
	}
	checkAPI := func(key string) int {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8087/v1/unknown", nil)
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+key)
		out := httptest.NewRecorder()
		svc.APIHandler().ServeHTTP(out, r)
		return out.Code
	}
	if checkAPI(key1) != 404 || checkAPI(key2) != 404 {
		t.Fatal("API 未接受新 Key")
	}
	replacement := "replacement-key-1234567890"
	w = dashboardRequest(t, h, "POST", "/ui/api/agent-keys/update", map[string]string{"id": id1, "key": replacement}, cookie, true, "http://127.0.0.1:8787")
	if w.Code != 200 || checkAPI(key1) != 401 || checkAPI(replacement) != 404 || checkAPI(key2) != 404 {
		t.Fatal("更换 Key 未独立生效")
	}
	key1 = replacement
	w = dashboardRequest(t, h, "POST", "/ui/api/agent-keys/revoke", map[string]string{"id": id1}, cookie, true, "http://127.0.0.1:8787")
	if w.Code != 200 || checkAPI(key1) != 401 || checkAPI(key2) != 404 || !store.validAgentKey(store.get().LocalKey) {
		t.Fatal("独立撤销失败")
	}
	reloaded, err := newStore(path)
	if err != nil || reloaded.validAgentKey(key1) || !reloaded.validAgentKey(key2) || !reloaded.validAgentKey(store.get().LocalKey) {
		t.Fatal("重载后 Key 状态错误")
	}
	legacy := svc.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	r := httptest.NewRequest("GET", "http://127.0.0.1/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+key2)
	out := httptest.NewRecorder()
	legacy.ServeHTTP(out, r)
	if out.Code != 204 {
		t.Fatal("兼容认证入口未接受新 Key")
	}
	old := store.get().LocalKey
	w = dashboardRequest(t, h, "POST", "/ui/api/agent-keys/revoke", map[string]string{"id": "default"}, cookie, true, "http://127.0.0.1:8787")
	if w.Code != 200 || store.validAgentKey(old) || !store.validAgentKey(key2) || store.validAgentKey("") {
		t.Fatal("历史 Key 删除失败")
	}
	reloaded, err = newStore(path)
	if err != nil || reloaded.validAgentKey(old) || !reloaded.validAgentKey(key2) {
		t.Fatal("删除历史 Key 后无法重启")
	}
	if reloaded.displayAgentKey() != key2 {
		t.Fatal("兼容入口未选择可用 Key")
	}
	w = dashboardRequest(t, h, "POST", "/ui/api/agent-keys/revoke", map[string]string{"id": id2}, cookie, true, "http://127.0.0.1:8787")
	if w.Code != 200 || store.validAgentKey(key2) || store.validAgentKey("") {
		t.Fatal("最后一个 Key 未删除")
	}
	reloaded, err = newStore(path)
	if err != nil || reloaded.validAgentKey("") || reloaded.displayAgentKey() != "" {
		t.Fatal("无 Key 状态重启失败")
	}
	if _, secret, err := reloaded.addAgentKey("新应用", ""); err != nil || !reloaded.validAgentKey(secret) {
		t.Fatal("无 Key 状态无法创建 Key")
	}
}
