package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUntrackedFilesAllowedAndPathCollisionsBlocked(t *testing.T) {
	u, base := repoFixture(t)
	ctx := context.Background()
	cmd := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = u.repoDir
		out, err := c.CombinedOutput()
		if err != nil { t.Fatalf("git %v failed: %v %s", args, err, out) }
		return strings.TrimSpace(string(out))
	}
	// Simulate a future upstream commit without touching network.
	if err := os.MkdirAll(filepath.Join(u.repoDir, "nested"), 0700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(u.repoDir, "nested", "new.txt"), []byte("upstream"), 0600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(u.repoDir, "new.json"), []byte("{}"), 0600); err != nil { t.Fatal(err) }
	cmd("add", "nested/new.txt", "new.json")
	cmd("commit", "-m", "future")
	target := cmd("rev-parse", "HEAD")
	cmd("reset", "--hard", base)
	if err := os.MkdirAll(filepath.Join(u.repoDir, "user-data"), 0700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(u.repoDir, "user-data", "备注.txt"), []byte("mine"), 0600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(u.repoDir, "custom.json"), []byte("mine"), 0600); err != nil { t.Fatal(err) }
	if err := u.checkSource(ctx); err != nil { t.Fatalf("untracked user files must not block: %v", err) }
	if err := u.checkUpdateCollisions(ctx, target); err != nil { t.Fatalf("unrelated files must be kept: %v", err) }

	// File at an upstream-added path must be reported and left untouched.
	userFile := filepath.Join(u.repoDir, "new.json")
	if err := os.WriteFile(userFile, []byte("private"), 0600); err != nil { t.Fatal(err) }
	if err := u.checkUpdateCollisions(ctx, target); err == nil || !strings.Contains(err.Error(), "new.json") { t.Fatalf("collision missing: %v", err) }
	if got, err := os.ReadFile(userFile); err != nil || string(got) != "private" { t.Fatalf("local file changed: %q %v", got, err) }
	if err := os.Remove(userFile); err != nil { t.Fatal(err) }

	// Even an empty local directory must not be replaced by upstream's file.
	if err := os.Mkdir(userFile, 0700); err != nil { t.Fatal(err) }
	if err := u.checkUpdateCollisions(ctx, target); err == nil || !strings.Contains(err.Error(), "new.json") { t.Fatalf("directory/file collision missing: %v", err) }
	if err := os.Remove(userFile); err != nil { t.Fatal(err) }

	// A file blocking a future upstream directory must also be rejected.
	if err := os.Remove(filepath.Join(u.repoDir, "nested")); err != nil && !os.IsNotExist(err) { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(u.repoDir, "nested"), []byte("private"), 0600); err != nil { t.Fatal(err) }
	if err := u.checkUpdateCollisions(ctx, target); err == nil || !strings.Contains(err.Error(), "nested") { t.Fatalf("ancestor collision missing: %v", err) }
	if err := os.Remove(filepath.Join(u.repoDir, "nested")); err != nil { t.Fatal(err) }

	// Modified tracked source remains protected.
	if err := os.WriteFile(filepath.Join(u.repoDir, "README"), []byte("local change"), 0600); err != nil { t.Fatal(err) }
	if err := u.checkSource(ctx); err == nil || !strings.Contains(err.Error(), "跟踪") { t.Fatalf("modified tracked file allowed: %v", err) }
}

func TestIgnoredUserFileCannotBeOverwritten(t *testing.T) {
	u, base := repoFixture(t)
	ctx := context.Background()
	cmd := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = u.repoDir
		out, err := c.CombinedOutput()
		if err != nil { t.Fatalf("git %v failed: %v %s", args, err, out) }
		return strings.TrimSpace(string(out))
	}
	if err := os.WriteFile(filepath.Join(u.repoDir, "secret.tmp"), []byte("upstream"), 0600); err != nil { t.Fatal(err) }
	cmd("add", "secret.tmp")
	cmd("commit", "-m", "add secret tmp")
	target := cmd("rev-parse", "HEAD")
	cmd("reset", "--hard", base)
	if err := os.WriteFile(filepath.Join(u.repoDir, ".git", "info", "exclude"), []byte("secret.tmp\n"), 0600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(u.repoDir, "secret.tmp"), []byte("private"), 0600); err != nil { t.Fatal(err) }
	if err := u.checkUpdateCollisions(ctx, target); err == nil || !strings.Contains(err.Error(), "secret.tmp") { t.Fatalf("ignored collision not protected: %v", err) }
}
