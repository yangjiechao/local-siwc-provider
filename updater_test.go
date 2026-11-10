package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoFixture(t *testing.T) (*UpdateManager, string) {
	t.Helper()
	repo := t.TempDir()
	cmd := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = repo
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	cmd("init", "-b", "main")
	cmd("config", "user.name", "Updater Test")
	cmd("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("/siwc-provider\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd("add", ".")
	cmd("commit", "-m", "initial")
	cmd("remote", "add", "origin", "https://github.com/yangjiechao/local-siwc-provider.git")
	sha := cmd("rev-parse", "HEAD")
	bin := filepath.Join(repo, "siwc-provider")
	if err := os.WriteFile(bin, []byte("dummy"), 0700); err != nil {
		t.Fatal(err)
	}
	return &UpdateManager{repoDir: repo, binaryPath: bin, apiURL: "", client: &http.Client{}, state: UpdateSnapshot{LocalCommit: sha, LocalVersion: "test (" + sha[:7] + ")", Status: "unchecked"}}, sha
}

func TestApprovedGitOrigin(t *testing.T) {
	for _, v := range []string{"https://github.com/yangjiechao/local-siwc-provider.git", "https://github.com/yangjiechao/local-siwc-provider", "git@github.com:yangjiechao/local-siwc-provider.git"} {
		if !approvedGitOrigin(v) {
			t.Errorf("legitimate origin rejected: %q", v)
		}
	}
	for _, v := range []string{"https://github.com/evil/local-siwc-provider.git", "https://github.com/yangjiechao/local-siwc-provider.git@evil.example/a", "file:///tmp/fake", "http://github.com/yangjiechao/local-siwc-provider.git", "git@evil.example:yangjiechao/local-siwc-provider.git"} {
		if approvedGitOrigin(v) {
			t.Errorf("unsafe origin accepted: %q", v)
		}
	}
}

func TestVersionCheckAndLocalChanges(t *testing.T) {
	u, local := repoFixture(t)
	latest := strings.Repeat("a", 40)
	var compareCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/commits/main":
			fmt.Fprintf(w, `{"sha":%q,"commit":{"committer":{"date":"2026-11-10T00:00:00Z"}}}`, latest)
		case "/compare/" + local + "..." + latest:
			compareCount++
			fmt.Fprint(w, `{"status":"ahead","ahead_by":3}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u.apiURL = server.URL
	u.client = server.Client()
	result := u.Check(context.Background())
	if result.Status != "available" || !result.CanUpdate || result.LatestCommit != latest || compareCount != 1 {
		t.Fatalf("wrong update comparison: %+v", result)
	}
	if err := os.WriteFile(filepath.Join(u.repoDir, "untracked.txt"), []byte("local edit"), 0600); err != nil {
		t.Fatal(err)
	}
	result = u.Check(context.Background())
	if result.Status != "manual" || result.CanUpdate || !strings.Contains(result.Message, "未提交") {
		t.Fatalf("dirty working tree should block auto upgrade: %+v", result)
	}
	if err := u.Apply(); err == nil {
		t.Fatal("Apply should refuse when update is not available")
	}
}

func TestVersionCheckCurrentAndDivergent(t *testing.T) {
	u, local := repoFixture(t)
	latest := local
	diffStatus := "identical"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/commits/main":
			fmt.Fprintf(w, `{"sha":%q}`, latest)
		case strings.HasPrefix(r.URL.Path, "/compare/"):
			fmt.Fprintf(w, `{"status":%q}`, diffStatus)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u.apiURL = server.URL
	u.client = server.Client()
	a := u.Check(context.Background())
	if a.Status != "current" || a.CanUpdate {
		t.Fatalf("expected current: %+v", a)
	}
	latest = strings.Repeat("b", 40)
	diffStatus = "diverged"
	a = u.Check(context.Background())
	if a.Status != "diverged" || a.CanUpdate {
		t.Fatalf("expected diverged: %+v", a)
	}
}

func TestVersionChecksGuardWrongOriginAndBinary(t *testing.T) {
	u, _ := repoFixture(t)
	ctx := context.Background()
	if err := u.checkSource(ctx); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("git", "remote", "set-url", "origin", "https://evil.example/repo.git")
	c.Dir = u.repoDir
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
	if err := u.checkSource(ctx); err == nil || !strings.Contains(err.Error(), "remote origin") {
		t.Fatalf("wrong origin should be blocked: %v", err)
	}
	u.binaryPath = filepath.Join(u.repoDir, "something-else")
	if err := u.checkSource(ctx); err == nil {
		t.Fatal("nonstandard executable should be blocked")
	}
}

func TestDashboardIncludesUpdaterControls(t *testing.T) {
	html, err := dashboardFiles.ReadFile("dashboard/index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := dashboardFiles.ReadFile("dashboard/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"localVersion", "githubVersion", "updateBadge", "updateMessage", "checkUpdateBtn", "applyUpdateBtn"} {
		if !strings.Contains(string(html), `id="`+id+`"`) {
			t.Errorf("missing updater control %q", id)
		}
		if !strings.Contains(string(js), "$('"+id+"')") {
			t.Errorf("missing JS wiring %q", id)
		}
	}
}
