package main

import (
    "strings"
    "testing"
)

// Ensure periodic dashboard data polling does not destroy native <details>
// disclosure state or scroll position when no model data has changed.
func TestProviderDetailsStateSurvivesAutoRefresh(t *testing.T) {
    js, err := dashboardFiles.ReadFile("dashboard/app.js")
    if err != nil { t.Fatal(err) }
    src := string(js)
    required := []string{
        "const expandedProviderModels = new Set()",
        "const providerModelScroll = new Map()",
        "function captureExpandedProviderModels()",
        "captureExpandedProviderModels();",
        "models.dataset.providerId=item.id",
        "models.open=expandedProviderModels.has(item.id)",
        "expandedProviderModels.add(item.id)",
        "expandedProviderModels.delete(item.id)",
        "expandedGrid.scrollTop=providerModelScroll.get(item.id)||0",
        "const providersChanged=JSON.stringify(configuredProviders)!==JSON.stringify(nextProviders)",
        "if(!providersHaveRendered||providersChanged)",
        "if(nextSIWCFingerprint!==siwcRenderFingerprint)",
    }
    for _,marker:=range required {
        if !strings.Contains(src,marker){t.Errorf("missing refresh state preservation: %s",marker)}
    }
}
