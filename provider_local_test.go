package main

import (
 "context"
 "encoding/json"
 "fmt"
 "net"
 "net/http"
 "net/http/httptest"
 "path/filepath"
 "strings"
 "testing"
)

func TestProviderLocalValidationAndPresetProtection(t *testing.T) {
 for _, raw := range []string{"http://127.0.0.1:11434/v1","http://localhost:1234/v1","http://192.168.1.24:9000/v1","http://nat-mini.local:8080/v1","http://[::1]:1234/v1"} {
   if err:=validateLocalProviderURL(raw);err!=nil { t.Errorf("rejected %s: %v",raw,err) }
 }
 for _,raw:=range []string{"https://api.openai.com/v1","http://8.8.8.8:11434/v1","http://example.com:11434/v1","http://localhost:8787/v1","http://127.0.0.1:8087/v1","http://127.0.0.1:80/v1","http://127.0.0.1:11434/v1?x=1","http://user:pw@localhost:11434/v1","http://127.0.0.1:11434/%2e%2e"} {
   if err:=validateLocalProviderURL(raw);err==nil {t.Errorf("accepted %s",raw)}
 }
 if err:=validateProviderURL("http://127.0.0.1:11434/v1");err==nil {t.Fatal("cloud unexpectedly accepts local HTTP")}
 for _,p:=range providerPresets {
   key:="test-key"
   if p.Kind=="local-openai-compatible" {key=""}
   if err:=validateExternalProvider(ExternalProvider{ID:p.ID+"-test",Name:p.Name,Kind:p.Kind,BaseURL:p.BaseURL,Preset:p.ID,APIKey:key,Enabled:true});err!=nil {t.Errorf("invalid official preset %s: %v",p.ID,err)}
 }
 if err:=validateExternalProvider(ExternalProvider{ID:"preset-spoof",Name:"Spoof",Kind:"local-openai-compatible",BaseURL:"http://127.0.0.1:7777/v1",Preset:"ollama"});err==nil {t.Fatal("preset spoof not rejected")}
}

func TestLocalModelCatalogAndInferenceWithNoAPIKey(t *testing.T) {
 upstream:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
   if got:=r.Header.Get("Authorization");got!=""{t.Errorf("local key leaked: %q",got)}
   switch r.URL.Path {
   case "/v1/models":fmt.Fprint(w,`{"object":"list","data":[{"id":"qwen3:8b"}]}`)
   case "/v1/chat/completions":
     var body map[string]any
     if err:=json.NewDecoder(r.Body).Decode(&body);err!=nil{t.Error(err)}
     if body["model"]!="qwen3:8b" {t.Errorf("wrong routed model: %v",body["model"])}
     fmt.Fprint(w,`{"id":"chatcmpl-local","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
   default: http.NotFound(w,r)
   }
 }))
 defer upstream.Close()
 store,err:=newStore(filepath.Join(t.TempDir(),"state.json"));if err!=nil{t.Fatal(err)}
 p:=ExternalProvider{ID:"ollama",Name:"Ollama",Kind:"local-openai-compatible",BaseURL:upstream.URL+"/v1",Enabled:true}
 if err:=store.upsertProvider(p);err!=nil{t.Fatal(err)}
 app:=newServer(newAuth(store));app.router.Refresh(context.Background())
 if x:=app.router.Models();len(x)!=1||x[0].ID!="ollama/qwen3:8b"{t.Fatalf("missing local catalog: %+v",x)}
 req:=httptest.NewRequest(http.MethodPost,"http://127.0.0.1:8087/v1/chat/completions",strings.NewReader(`{"model":"ollama/qwen3:8b","messages":[{"role":"user","content":"hi"}]}`))
 req.RemoteAddr="127.0.0.1:50055";req.Header.Set("Authorization","Bearer "+store.get().LocalKey)
 rec:=httptest.NewRecorder();app.APIHandler().ServeHTTP(rec,req)
 if rec.Code!=200||!strings.Contains(rec.Body.String(),"chatcmpl-local"){t.Fatalf("local inference %d: %s",rec.Code,rec.Body.String())}
 list:=app.router.Providers()
 if len(list)!=1||list[0].HasAPIKey {t.Fatalf("local anonymous provider state wrong: %+v",list)}
}

func TestLocalDNSBlocksPublicIPs(t *testing.T) {
 if _,err:=dialLocalProvider(context.Background(),"tcp","8.8.8.8:11434");err==nil{t.Fatal("dialed public IP")}
 // A loopback socket is acceptable for local model discovery.
 listener,err:=net.Listen("tcp4","127.0.0.1:0");if err!=nil{t.Fatal(err)}
 defer listener.Close()
 conn,err:=dialLocalProvider(context.Background(),"tcp",listener.Addr().String())
 if err!=nil{t.Fatal(err)}
 conn.Close()
}
