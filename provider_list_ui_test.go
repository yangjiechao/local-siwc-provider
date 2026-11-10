package main

import (
  "strings"
  "testing"
)

func TestProviderListFirstDialogAssets(t *testing.T) {
  h,e:=dashboardFiles.ReadFile("dashboard/index.html");if e!=nil{t.Fatal(e)}
  j,e:=dashboardFiles.ReadFile("dashboard/app.js");if e!=nil{t.Fatal(e)}
  html,js:=string(h),string(j)
  for _,id:=range []string{"providerList","providerSearch","providerSummary","providerDialog","providerDialogTitle","addProviderBtn","closeProviderBtn","providerForm"} {
    if !strings.Contains(html,"id=\""+id+"\""){t.Errorf("missing control %s",id)}
  }
  if strings.Index(html,"id=\"providerList\"") >= strings.Index(html,"id=\"providerForm\""){t.Fatal("provider list must precede modal")}
  for _,fn:=range []string{"showModal()","closeProviderDialog()","required=false","api_key:''","providerSearch"}{
    if !strings.Contains(js,fn){t.Errorf("missing modal/list behavior %s",fn)}
  }
}
