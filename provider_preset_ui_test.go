package main

import (
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "path/filepath"
 "strings"
 "testing"
)

func TestProviderPresetCatalogAndLocalLogos(t *testing.T){
 s,err:=newStore(filepath.Join(t.TempDir(),"state.json"));if err!=nil{t.Fatal(err)}
 app:=newServer(newAuth(s));app.dashboard=newDashboard(app)
 cookie:=adminCookie(t,app)
 w:=adminRequest(t,app.AdminHandler(),"GET","/ui/api/provider-presets",nil,cookie)
 if w.Code!=200{t.Fatalf("presets %d: %s",w.Code,w.Body.String())}
 var d struct {Presets []ProviderPreset `json:"presets"`}
 if err:=json.Unmarshal(w.Body.Bytes(),&d);err!=nil{t.Fatal(err)}
 if len(d.Presets)<10{t.Fatalf("missing provider presets: %+v",d)}
 for _,p:=range d.Presets {
  if p.Logo==""{continue}
  if _,err:=dashboardFiles.ReadFile("dashboard/assets/logos/"+p.Logo+".svg");err!=nil{t.Errorf("logo missing for %s: %v",p.ID,err)}
 }
 res:=httptest.NewRecorder()
 req:=httptest.NewRequest(http.MethodGet,"http://127.0.0.1:8787/ui/assets/logos/openai.svg",nil)
 app.AdminHandler().ServeHTTP(res,req)
 if res.Code!=200||!strings.Contains(res.Body.String(),"<svg"){t.Fatalf("logo asset not served: %d",res.Code)}
}
func TestProviderPresetUIBindings(t *testing.T){
 h,e:=dashboardFiles.ReadFile("dashboard/index.html");if e!=nil{t.Fatal(e)}
 j,e:=dashboardFiles.ReadFile("dashboard/app.js");if e!=nil{t.Fatal(e)}
 html,js:=string(h),string(j)
 for _,id:=range []string{"providerPreset","providerPresetPreview","providerPresetURL","providerPresetLogo","providerBaseField","providerKind","providerBase","providerSecurityHint"}{
  if !strings.Contains(html,"id=\""+id+"\""){t.Errorf("missing %s",id)}
 }
 for _,marker:=range []string{"loadProviderPresets()","custom-cloud","custom-local","custom-video","makeBrandLogo","preset:preset?.id||'custom'"}{
  if !strings.Contains(js,marker){t.Errorf("missing UI behavior %s",marker)}
 }
}
