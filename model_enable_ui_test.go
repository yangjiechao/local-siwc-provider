package main

import (
	"strings"
	"testing"
)

func TestPerProviderModelCheckboxDashboardAssets(t *testing.T){
	html,err:=dashboardFiles.ReadFile("dashboard/index.html")
	if err!=nil{t.Fatal(err)}
	js,err:=dashboardFiles.ReadFile("dashboard/app.js")
	if err!=nil{t.Fatal(err)}
	css,err:=dashboardFiles.ReadFile("dashboard/app.css")
	if err!=nil{t.Fatal(err)}
	for _,id:=range []string{"siwcModelPanel","siwcModelHeading","siwcModelStatus","siwcModelToggleList"}{
		if !strings.Contains(string(html),"id=\""+id+"\""){t.Errorf("missing model-toggle UI element %s",id)}
	}
	for _,expr:=range []string{"renderSIWCModelToggles","modelCheckbox","models/enable","siwc_disabled_models","disabled_models","siwc_models","input.checked=!disabledModels.includes(model)"}{
		if !strings.Contains(string(js),expr){t.Errorf("missing model-toggle behavior %s",expr)}
	}
	if !strings.Contains(string(css),".model-toggle-item"){t.Fatal("missing model toggle styling")}
}
