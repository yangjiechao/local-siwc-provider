package main

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func dashboardRequest(t *testing.T, h http.Handler, method, path string, data any, cookie *http.Cookie, header bool, origin string) *httptest.ResponseRecorder {
    t.Helper()
    var buf []byte
    if data != nil { var err error; buf, err = json.Marshal(data); if err != nil {t.Fatal(err)} }
    r := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, bytes.NewReader(buf))
    if data != nil {r.Header.Set("Content-Type","application/json")}
    if header {r.Header.Set(dashboardHeader,"1")}
    if origin != "" {r.Header.Set("Origin",origin)}
    if cookie != nil {r.AddCookie(cookie)}
    w := httptest.NewRecorder()
    h.ServeHTTP(w,r)
    return w
}

func TestDashboardAuthAndService(t *testing.T) {
    store,err:=newStore(filepath.Join(t.TempDir(),"state.json")); if err!=nil {t.Fatal(err)}
    svc:=newServer(newAuth(store));svc.apiEnabled.Store(false);svc.dashboard=newDashboard(svc);h:=svc.Handler()
    for _,p:=range []string{"/ui/","/ui/app.js","/ui/app.css"} {
        if w:=dashboardRequest(t,h,"GET",p,nil,nil,false,"");w.Code!=200 {t.Errorf("%s: %d",p,w.Code)}
    }
    if w:=dashboardRequest(t,h,"GET","/ui/api/key",nil,nil,false,"");w.Code!=401 {t.Fatalf("unauthorized key: %d",w.Code)}
    if w:=dashboardRequest(t,h,"POST","/ui/api/session",map[string]string{"key":store.get().LocalKey},nil,true,"");w.Code!=401 {t.Fatal("Agent key unlocked admin UI")}
    if w:=dashboardRequest(t,h,"POST","/ui/api/session",map[string]string{"key":store.get().AdminKey},nil,false,"");w.Code!=403 {t.Fatal("no CSRF check")}
    if w:=dashboardRequest(t,h,"POST","/ui/api/session",map[string]string{"key":store.get().AdminKey},nil,true,"https://evil.example");w.Code!=403 {t.Fatal("Origin bypass")}
    unlocked:=dashboardRequest(t,h,"POST","/ui/api/session",map[string]string{"key":store.get().AdminKey},nil,true,"http://127.0.0.1:8787")
    if unlocked.Code!=200 {t.Fatalf("unlock: %d %s",unlocked.Code,unlocked.Body.String())}
    cookies:=unlocked.Result().Cookies(); if len(cookies)!=1 || !cookies[0].HttpOnly || cookies[0].SameSite!=http.SameSiteStrictMode {t.Fatal("insecure session cookie")}
    c:=cookies[0]
    if w:=dashboardRequest(t,h,"GET","/ui/api/status",nil,c,false,"");w.Code!=200 {t.Fatal("status failed")}
    if w:=dashboardRequest(t,h,"POST","/ui/api/service",map[string]bool{"enabled":true},c,true,"");w.Code!=409 {t.Fatal("API started without login")}
    if svc.apiEnabled.Load() {t.Fatal("API enabled without ChatGPT")}
    if err:=store.saveCredential(&Credential{Email:"test@example.com",ClientID:"oaiapp_test",Scopes:requiredScope,ExpiresAt:time.Now().Add(time.Hour)});err!=nil {t.Fatal(err)}
    if w:=dashboardRequest(t,h,"POST","/ui/api/service",map[string]bool{"enabled":true},c,true,"");w.Code!=200 || !svc.apiEnabled.Load() {t.Fatal("API did not start")}
    if w:=dashboardRequest(t,h,"POST","/ui/api/service",map[string]bool{"enabled":false},c,true,"");w.Code!=200 || svc.apiEnabled.Load() {t.Fatal("API did not stop")}
    k:=dashboardRequest(t,h,"GET","/ui/api/key",nil,c,false,"")
    if k.Code!=200 || !strings.Contains(k.Body.String(),store.get().LocalKey) {t.Fatal("key response incorrect")}
    if w:=dashboardRequest(t,h,"POST","/ui/api/session/logout",map[string]string{},c,true,"");w.Code!=200 {t.Fatal("lock failed")}
    if w:=dashboardRequest(t,h,"GET","/ui/api/status",nil,c,false,"");w.Code!=401 {t.Fatal("locked session still active")}
    if w:=dashboardRequest(t,h,"GET","/v1/models",nil,nil,false,"");w.Code!=503 {t.Fatal("stopped API still active")}
}

func TestDashboardHostAndStateMigration(t *testing.T) {
    path:=filepath.Join(t.TempDir(),"state.json")
    payload,_:=json.Marshal(State{HostID:"urn:uuid:legacy",LocalKey:"old-agent-key"})
    if err:=os.WriteFile(path,payload,0600);err!=nil {t.Fatal(err)}
    store,err:=newStore(path);if err!=nil {t.Fatal(err)}
    if store.get().LocalKey!="old-agent-key"||store.get().AdminKey=="" {t.Fatal("failed old-store migration")}
    again,err:=newStore(path);if err!=nil||again.get().AdminKey!=store.get().AdminKey {t.Fatal("unstable admin key")}
    s:=newServer(newAuth(store));s.dashboard=newDashboard(s)
    r:=httptest.NewRequest("GET","http://evil.example/ui/",nil);w:=httptest.NewRecorder()
    s.Handler().ServeHTTP(w,r)
    if w.Code!=403 {t.Fatal("DNS rebinding protection failed")}
}
