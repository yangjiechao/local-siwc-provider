package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderOrderPersistAndRouting(t *testing.T) {
	store, err := newStore(filepath.Join(t.TempDir(),"state.json"))
	if err != nil { t.Fatal(err) }
	for _, item := range []struct{id, key string}{
		{"alpha","key-alpha"},{"beta","key-beta"},{"gamma","key-gamma"},
	} {
		err = store.upsertProvider(ExternalProvider{
			ID:item.id, Name:item.id, Kind:"openai-compatible",
			BaseURL:"https://"+item.id+".example.org/v1",APIKey:item.key,
			Enabled:true,ManualModels:[]string{"same","only-"+item.id},
		})
		if err != nil {t.Fatal(err)}
	}
	router:=newRouter(newAuth(store))
	assertRoute:=func(model,want string){
		t.Helper()
		p,actual,err:=router.Resolve(model)
		if err!=nil||p==nil||p.ID!=want||actual=="" {t.Fatalf("resolve %q expected %q: %+v %q %v",model,want,p,actual,err)}
	}
	assertRoute("same","alpha")
	for _,invalid:=range [][]string{
		{"beta","alpha"}, {"beta","beta","gamma"},
		{"beta","unknown","gamma"}, {"alpha","beta","beta"},
	} {
		if err:=store.reorderProviders(invalid);err==nil {t.Errorf("accepted invalid provider ordering: %+v",invalid)}
	}
	assertRoute("same","alpha")
	if err:=store.reorderProviders([]string{"gamma","beta","alpha"});err!=nil{t.Fatal(err)}
	assertRoute("same","gamma")
	assertRoute("alpha/same","alpha")
	assertRoute("beta/same","beta")
	assertRoute("gamma/only-gamma","gamma")
	saved:=store.get().Providers
	if saved[0].APIKey!="key-gamma" || saved[1].APIKey!="key-beta" || saved[2].APIKey!="key-alpha" {
		t.Fatalf("reorder changed secrets: %+v",saved)
	}
	reloaded,err:=newStore(store.path);if err!=nil{t.Fatal(err)}
	if reloaded.get().Providers[0].ID!="gamma"{t.Fatalf("priority did not persist: %+v",reloaded.get().Providers)}
	restarted:=newRouter(newAuth(reloaded))
	p,_,err:=restarted.Resolve("same")
	if err!=nil||p==nil||p.ID!="gamma"{t.Fatalf("restart lost priority: %+v %v",p,err)}
	gamma:=store.get().Providers[0]
	gamma.Enabled=false
	gamma.APIKey=""
	if err:=store.upsertProvider(gamma);err!=nil{t.Fatal(err)}
	assertRoute("same","beta")
	if err:=store.saveCredential(&Credential{
		AccessToken:"fake",Scopes:requiredScope,ExpiresAt:time.Now().Add(time.Hour),
	});err!=nil{t.Fatal(err)}
	router.mu.Lock()
	router.snap.own=[]string{"same"}
	router.snap.ownOK=true
	router.mu.Unlock()
	p,actual,err:=router.Resolve("same")
	if err!=nil||p!=nil||actual!="same"{t.Fatalf("SIWC is not first: %+v %q %v",p,actual,err)}
	router.mu.Lock()
	router.snap.ownOK=false
	router.mu.Unlock()
	if _,_,err=router.Resolve("same");err==nil||!strings.Contains(err.Error(),"SIWC"){
		t.Fatalf("missing SIWC model catalog silently falls back to paid provider: %v",err)
	}
}

func TestProviderReorderDashboardAuthAndValidation(t *testing.T) {
	app,_,_:=runtimeFixture(t)
	for _,id:=range []string{"one","two"} {
		if err:=app.auth.store.upsertProvider(ExternalProvider{
			ID:id,Name:id,Kind:"openai-compatible",BaseURL:"https://"+id+".example.org/v1",
			APIKey:"secret-"+id,Enabled:true,ManualModels:[]string{"shared"},
		});err!=nil{t.Fatal(err)}
	}
	admin:=app.AdminHandler()
	body:=map[string]any{"ids":[]string{"two","one"}}
	if got:=adminRequest(t,admin,"POST","/ui/api/providers/reorder",body,nil);got.Code!=401 {
		t.Fatalf("unauthenticated reorder accepted %d",got.Code)
	}
	cookie:=adminCookie(t,app)
	if got:=adminRequest(t,admin,"POST","/ui/api/providers/reorder",map[string]any{"ids":[]string{"two","two"}},cookie);got.Code!=409 {
		t.Fatalf("duplicate order accepted %d: %s",got.Code,got.Body.String())
	}
	response:=adminRequest(t,admin,"POST","/ui/api/providers/reorder",body,cookie)
	if response.Code!=200 {t.Fatalf("reorder failed %d: %s",response.Code,response.Body.String())}
	if strings.Contains(response.Body.String(),"secret-") {t.Fatal("dashboard leaked saved provider key")}
	if app.auth.store.get().Providers[0].ID!="two" {t.Fatal("reorder did not persist")}
	req:=adminRequest(t,app.APIHandler(),http.MethodPost,"/ui/api/providers/reorder",body,nil)
	if req.Code==200{t.Fatal("reorder exposed to Agent API")}
}

func TestProviderPriorityUIAssets(t *testing.T) {
	h,err:=dashboardFiles.ReadFile("dashboard/index.html")
	if err!=nil{t.Fatal(err)}
	j,err:=dashboardFiles.ReadFile("dashboard/app.js")
	if err!=nil{t.Fatal(err)}
	c,err:=dashboardFiles.ReadFile("dashboard/app.css")
	if err!=nil{t.Fatal(err)}
	for _,match:=range []string{"moveProviderPriority","providers/reorder","provider-order-button","const canReorder=q.length===0"} {
		if !strings.Contains(string(j),match){t.Errorf("missing ordering UI %q",match)}
	}
	if !strings.Contains(string(h),"SIWC 固定第一位"){t.Error("SIWC priority explanation missing")}
	if !strings.Contains(string(c),"--accent:#2e5b88"){t.Error("calm blue palette not installed")}
}
