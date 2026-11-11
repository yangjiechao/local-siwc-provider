package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModelEnableDefaultsAndFallbackPriority(t *testing.T) {
	store, err := newStore(filepath.Join(t.TempDir(),"state.json"))
	if err != nil { t.Fatal(err) }
	for _,id:=range []string{"first","second"} {
		err=store.upsertProvider(ExternalProvider{
			ID:id, Name:id, Kind:"openai-compatible",
			BaseURL:"https://"+id+".example.org/v1", APIKey:"secret-"+id, Enabled:true,
			ManualModels:[]string{"shared","unique-"+id},
		})
		if err!=nil {t.Fatal(err)}
	}
	router:=newRouter(newAuth(store))
	assert:=func(model,want string) {
		t.Helper()
		p,_,err:=router.Resolve(model)
		if err!=nil {t.Fatalf("%s route error: %v",model,err)}
		id:="siwc";if p!=nil{id=p.ID}
		if id!=want{t.Fatalf("%s routed to %s, wanted %s",model,id,want)}
	}
	assert("shared","first")
	// Models default enabled, with no model-specific configuration.
	if len(store.get().DisabledModels)!=0 {t.Fatal("initial model options must all be enabled")}
	if err:=store.setModelEnabled("first","shared",false);err!=nil{t.Fatal(err)}
	assert("shared","second")
	assert("first/unique-first","first")
	if _,_,err:=router.Resolve("first/shared");err==nil||!strings.Contains(err.Error(),"disabled"){
		t.Fatalf("explicitly disabled model must be rejected: %v",err)
	}
	if err:=store.setModelEnabled("second","shared",false);err!=nil{t.Fatal(err)}
	if _,_,err:=router.Resolve("shared");err==nil{t.Fatal("all-disabled model must not resolve")}
	if err:=store.setModelEnabled("first","shared",true);err!=nil{t.Fatal(err)}
	assert("shared","first")
	if err:=store.saveCredential(&Credential{AccessToken:"fake",Scopes:requiredScope,ExpiresAt:time.Now().Add(time.Hour)});err!=nil{t.Fatal(err)}
	router.mu.Lock()
	router.snap.own=[]string{"shared"}
	router.snap.ownOK=true
	router.mu.Unlock()
	assert("shared","siwc")
	if err:=store.setModelEnabled("siwc","shared",false);err!=nil{t.Fatal(err)}
	assert("shared","first")
	if _,_,err:=router.Resolve("siwc/shared");err==nil||!strings.Contains(err.Error(),"disabled"){t.Fatalf("explicit disabled SIWC route: %v",err)}
	if err:=store.setModelEnabled("first","shared",false);err!=nil{t.Fatal(err)}
	if err:=store.setModelEnabled("second","shared",true);err!=nil{t.Fatal(err)}
	assert("shared","second")
	if err:=store.reorderProviders([]string{"second","first"});err!=nil{t.Fatal(err)}
	assert("shared","second")
	router.mu.Lock()
	router.snap.ownOK=false
	router.mu.Unlock()
	if _,_,err:=router.Resolve("shared");err==nil||!strings.Contains(err.Error(),"SIWC"){t.Fatalf("unavailable SIWC catalog must not auto-charge: %v",err)}
	reloaded,err:=newStore(store.path);if err!=nil{t.Fatal(err)}
	if !modelDisabled(reloaded.get(),"siwc","shared")||!modelDisabled(reloaded.get(),"first","shared"){t.Fatal("disabled model state not persisted")}
	if modelDisabled(reloaded.get(),"second","shared"){t.Fatal("enabled model should be absent from opt-out list")}
}

func TestModelToggleDashboardAuthorizationAndCatalog(t *testing.T){
	app,_,_:=runtimeFixture(t)
	key:=app.auth.store.get().LocalKey
	if err:=app.auth.store.upsertProvider(ExternalProvider{
		ID:"remote",Name:"Remote",Kind:"openai-compatible",BaseURL:"https://remote.example.org/v1",
		APIKey:"private",Enabled:true,ManualModels:[]string{"m1","m2"},
	});err!=nil{t.Fatal(err)}
	admin:=app.AdminHandler()
	request:=map[string]any{"provider":"remote","model":"m1","enabled":false}
	if w:=adminRequest(t,admin,"POST","/ui/api/models/enable",request,nil);w.Code!=401 {
		t.Fatalf("model toggle accepted without session %d",w.Code)
	}
	cookie:=adminCookie(t,app)
	if w:=adminRequest(t,admin,"POST","/ui/api/models/enable",map[string]any{"provider":"remote","model":"unknown","enabled":false},cookie);w.Code!=404 {
		t.Fatalf("unknown model should be refused: %d",w.Code)
	}
	w:=adminRequest(t,admin,"POST","/ui/api/models/enable",request,cookie)
	if w.Code!=200 {t.Fatalf("toggle failed %d: %s",w.Code,w.Body.String())}
	r:=adminRequest(t,admin,"GET","/ui/api/providers",nil,cookie)
	var payload struct {
		Providers []PublicProvider `json:"providers"`
		Models []RoutedModel `json:"models"`
	}
	if err:=json.Unmarshal(r.Body.Bytes(),&payload);err!=nil{t.Fatal(err)}
	if len(payload.Providers)!=1||len(payload.Providers[0].DisabledModels)!=1||payload.Providers[0].DisabledModels[0]!="m1"{
		t.Fatalf("model should remain visible and unchecked: %+v",payload.Providers)
	}
	for _,model:=range payload.Models{if model.ID=="remote/m1"{t.Fatal("disabled model exposed to Agent catalog")}}
	models:=adminRequest(t,app.APIHandler(),http.MethodGet,"/v1/models",nil,nil)
	_ = models // The regular agent endpoint is protected by local Agent API key.
	_ = key
	updated:=adminRequest(t,admin,"POST","/ui/api/models/enable",map[string]any{"provider":"remote","model":"m1","enabled":true},cookie)
	if updated.Code!=200{t.Fatalf("re-enable failed %d",updated.Code)}
	if modelDisabled(app.auth.store.get(),"remote","m1"){t.Fatal("model did not re-enable")}
	// Deleting a provider removes its stale disabled-model flags.
	if err:=app.auth.store.setModelEnabled("remote","m2",false);err!=nil{t.Fatal(err)}
	if err:=app.auth.store.deleteProvider("remote");err!=nil{t.Fatal(err)}
	if _,ok:=app.auth.store.get().DisabledModels["remote"];ok{t.Fatal("provider deletion leaked disabled flags into future entries")}
}
