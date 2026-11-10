package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// buildCommit is set by start.command and by the in-app updater at build time.
// If omitted, the running binary cannot reliably be matched to a Git checkout.
var buildCommit = ""

const githubRepoAPI = "https://api.github.com/repos/yangjiechao/local-siwc-provider"
const githubRepoWeb = "https://github.com/yangjiechao/local-siwc-provider"
const devVersion = "0.4.0-dev"

var commitIDPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

type UpdateSnapshot struct {
	LocalVersion string `json:"local_version"`
	LocalCommit  string `json:"local_commit"`
	LatestCommit string `json:"latest_commit"`
	LatestDate   string `json:"latest_date,omitempty"`
	Status       string `json:"status"`
	Message      string `json:"message"`
	Phase        string `json:"phase,omitempty"`
	CheckedAt    string `json:"checked_at,omitempty"`
	CanUpdate    bool   `json:"can_update"`
	Busy         bool   `json:"busy"`
	GithubURL    string `json:"github_url"`
}

type UpdateManager struct {
	mu         sync.Mutex
	state      UpdateSnapshot
	client     *http.Client
	apiURL     string // fixed Github host in production, overridable by local tests
	repoDir    string
	binaryPath string
	control    chan<- lifecycleAction
}

func newUpdateManager(control chan<- lifecycleAction) *UpdateManager {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	} else {
		exe, _ = filepath.EvalSymlinks(exe)
	}
	repoDir := filepath.Dir(exe)
	local := strings.TrimSpace(buildCommit)
	if !commitIDPattern.MatchString(local) {
		local = ""
	}
	version := devVersion
	if local != "" {
		version += " (" + local[:7] + ")"
	} else {
		version += " (未知构建提交)"
	}
	return &UpdateManager{client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, apiURL: githubRepoAPI, repoDir: repoDir, binaryPath: exe, control: control,
		state: UpdateSnapshot{LocalVersion: version, LocalCommit: local, Status: "unchecked", Message: "点击检查更新，从 GitHub main 获取最新提交。", GithubURL: githubRepoWeb + "/commits/main"}}
}

func (u *UpdateManager) snapshot() UpdateSnapshot { u.mu.Lock(); defer u.mu.Unlock(); return u.state }
func (u *UpdateManager) setState(f func(*UpdateSnapshot)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	f(&u.state)
}

func (u *UpdateManager) githubJSON(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.apiURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "local-siwc-provider-version-check")
	r, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach GitHub: %w", err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		// GitHub's REST API may return HTTP 403 on the unauthenticated
		// 60-request/hour quota. Preserve its diagnostic rather than labelling
		// every forbidden response as a network error.
		payload, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		var failure struct { Message string `json:"message"` }
		_ = json.Unmarshal(payload, &failure)
		message := strings.TrimSpace(failure.Message)
		if r.Header.Get("X-RateLimit-Remaining") == "0" {
			message = "GitHub REST API 请求额度已耗尽；使用 Git 克隆安装时可通过 git fetch 避开此限制"
		}
		if message == "" {
			message = http.StatusText(r.StatusCode)
		}
		if len(message) > 250 { message = message[:250] }
		return fmt.Errorf("GitHub HTTP %d: %s", r.StatusCode, message)
	}
	return json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(into)
}

// gitLatest avoids GitHub's unauthenticated REST API quota on standard
// installations. An eligible clone fetches the pinned origin into FETCH_HEAD
// without changing the checked-out files; other installations can at least
// display the current public branch SHA via ls-remote.
func (u *UpdateManager) gitLatest(ctx context.Context, eligible bool) (sha, date string, err error) {
	if eligible {
		if _, err := gitCmd(ctx, u.repoDir, "fetch", "--no-tags", "origin", "main"); err != nil {
			return "", "", fmt.Errorf("Git 检查远端提交失败：%w", err)
		}
		sha, err = gitCmd(ctx, u.repoDir, "rev-parse", "FETCH_HEAD")
		if err != nil { return "", "", err }
		if !commitIDPattern.MatchString(sha) { return "", "", errors.New("Git fetch returned invalid commit SHA") }
		date, _ = gitCmd(ctx, u.repoDir, "show", "-s", "--format=%cI", sha)
		return sha, date, nil
	}
	out, err := gitCmd(ctx, u.repoDir, "ls-remote", "--heads", githubRepoWeb+".git", "refs/heads/main")
	if err != nil { return "", "", fmt.Errorf("无法通过 Git 获取最新版本：%w", err) }
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[1] != "refs/heads/main" || !commitIDPattern.MatchString(fields[0]) {
		return "", "", errors.New("Git ls-remote did not return the official main branch")
	}
	return fields[0], "", nil
}

// compareGit compares locally fetched commits without another GitHub API call.
// The labels mirror the GitHub compare endpoint used by test/API fallback.
func (u *UpdateManager) compareGit(ctx context.Context, local, latest string) (string, int, error) {
	if local == latest { return "identical", 0, nil }
	if !commitIDPattern.MatchString(local) || !commitIDPattern.MatchString(latest) {
		return "", 0, errors.New("无法比较无效的 Git 提交")
	}
	if _, err := gitCmd(ctx, u.repoDir, "merge-base", "--is-ancestor", local, latest); err == nil {
		countStr, err := gitCmd(ctx, u.repoDir, "rev-list", "--count", local+".."+latest)
		if err != nil { return "", 0, err }
		var n int
		if _, err := fmt.Sscanf(countStr, "%d", &n); err != nil { return "", 0, err }
		return "ahead", n, nil
	}
	if _, err := gitCmd(ctx, u.repoDir, "merge-base", "--is-ancestor", latest, local); err == nil {
		return "behind", 0, nil
	}
	return "diverged", 0, nil
}

func shortID(id string) string {
	if len(id) >= 7 {
		return id[:7]
	}
	return id
}

func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	b, err := cmd.CombinedOutput()
	if err != nil {
		explanation := strings.TrimSpace(string(b))
		if len(explanation) > 350 {
			explanation = explanation[:350]
		}
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, explanation)
	}
	return strings.TrimSpace(string(b)), nil
}

func approvedGitOrigin(s string) bool {
	switch strings.TrimSpace(s) {
	case "https://github.com/yangjiechao/local-siwc-provider.git", "https://github.com/yangjiechao/local-siwc-provider", "git@github.com:yangjiechao/local-siwc-provider.git":
		return true
	}
	return false
}

func (u *UpdateManager) checkSource(ctx context.Context) error {
	if !commitIDPattern.MatchString(u.state.LocalCommit) {
		return errors.New("当前程序没有内嵌构建提交号；请通过 start.command 重新构建")
	}
	if runtime.GOOS == "windows" {
		return errors.New("Windows 暂不支持运行中自动替换可执行程序，请手动安装更新")
	}
	if u.binaryPath == "" || filepath.Base(u.binaryPath) != "siwc-provider" {
		return errors.New("仅支持从仓库编译的 siwc-provider 可执行文件；go run 和独立安装包暂不支持一键更新")
	}
	if _, err := os.Stat(filepath.Join(u.repoDir, ".git")); err != nil {
		return errors.New("未找到 Git 仓库，无法安全地自动更新")
	}
	root, err := gitCmd(ctx, u.repoDir, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(root) != filepath.Clean(u.repoDir) {
		return errors.New("程序不在仓库根目录")
	}
	branch, err := gitCmd(ctx, u.repoDir, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch != "main" {
		return errors.New("只支持 main 分支自动更新")
	}
	origin, err := gitCmd(ctx, u.repoDir, "remote", "get-url", "origin")
	if err != nil || !approvedGitOrigin(origin) {
		return errors.New("Git remote origin 不是项目官方仓库，自动更新已禁用")
	}
	head, err := gitCmd(ctx, u.repoDir, "rev-parse", "HEAD")
	if err != nil || head != u.state.LocalCommit {
		return errors.New("正在运行的二进制与仓库源码版本不一致；请用 start.command 重新启动")
	}
	dirty, err := gitCmd(ctx, u.repoDir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if dirty != "" {
		return errors.New("源码目录包含本地未提交修改，请先提交、暂存或移走后再更新")
	}
	if _, err := exec.LookPath("go"); err != nil {
		return errors.New("一键源码更新需要本机安装 Go 1.23+；否则请下载预编译版本")
	}
	return nil
}

func (u *UpdateManager) Check(ctx context.Context) UpdateSnapshot {
	u.mu.Lock()
	if u.state.Busy {
		s := u.state
		u.mu.Unlock()
		return s
	}
	u.state.Status = "checking"
	u.state.Message = "正在查询 GitHub 最新提交…"
	u.state.CanUpdate = false
	u.mu.Unlock()

	var latest struct {
		SHA string `json:"sha"`
		Commit struct {
			Committer struct { Date string `json:"date"` } `json:"committer"`
		} `json:"commit"`
	}
	// A real installation uses the Git protocol and local ancestry checks.
	// The injected HTTP endpoint is retained for isolated contract tests.
	usingGit := u.apiURL == githubRepoAPI
	sourceErr := u.checkSource(ctx)
	var err error
	if usingGit {
		var sha, date string
		sha, date, err = u.gitLatest(ctx, sourceErr == nil)
		if err == nil {
			latest.SHA = sha
			latest.Commit.Committer.Date = date
		} else {
			// Git may be missing or blocked. REST remains a diagnostic fallback.
			gitErr := err
			err = u.githubJSON(ctx, "/commits/main", &latest)
			if err != nil { err = fmt.Errorf("Git 更新检查失败：%v；REST 备用查询失败：%w", gitErr, err) }
			usingGit = false
		}
	} else {
		err = u.githubJSON(ctx, "/commits/main", &latest)
	}

	status, msg, can := "error", "", false
	if err == nil && !commitIDPattern.MatchString(latest.SHA) {
		err = errors.New("GitHub 返回的提交号无效")
	}
	if err == nil {
		// Recheck after network access in case a user changed the checkout.
		sourceErr = u.checkSource(ctx)
		switch {
		case sourceErr != nil:
			status = "manual"
			msg = sourceErr.Error()
		case latest.SHA == u.state.LocalCommit:
			status = "current"
			msg = "已经是 GitHub main 最新版本。"
		default:
			var diff struct {
				Status string `json:"status"`
				AheadBy int `json:"ahead_by"`
			}
			if usingGit {
				diff.Status, diff.AheadBy, err = u.compareGit(ctx, u.state.LocalCommit, latest.SHA)
			} else {
				err = u.githubJSON(ctx, "/compare/"+u.state.LocalCommit+"..."+latest.SHA, &diff)
				if err != nil && u.apiURL == githubRepoAPI {
					// A valid public Git clone can still compare locally if the
					// secondary GitHub REST endpoint is rate limited.
					var fetchErr error
					_, _, fetchErr = u.gitLatest(ctx, true)
					if fetchErr == nil {
						diff.Status, diff.AheadBy, err = u.compareGit(ctx, u.state.LocalCommit, latest.SHA)
					}
				}
			}
			if err == nil {
				switch diff.Status {
				case "ahead":
					status = "available"
					can = true
					msg = fmt.Sprintf("发现 %d 个新提交，可安全快进更新。", diff.AheadBy)
				case "identical":
					status = "current"
					msg = "已经是最新版本。"
				case "behind":
					status = "ahead"
					msg = "本地提交领先于 GitHub，不能自动覆盖。"
				default:
					status = "diverged"
					msg = "本地版本与 GitHub 已分叉，不自动覆盖。"
				}
			}
		}
	}
	if err != nil { msg = err.Error() }
	u.setState(func(s *UpdateSnapshot) {
		s.LatestCommit = latest.SHA
		s.LatestDate = latest.Commit.Committer.Date
		s.Status = status
		s.Message = msg
		s.CanUpdate = can
		s.CheckedAt = time.Now().Format(time.RFC3339)
	})
	return u.snapshot()
}

func (u *UpdateManager) setPhase(phase, msg string) {
	u.setState(func(s *UpdateSnapshot) { s.Phase = phase; s.Message = msg })
}

// Apply performs a verified fast-forward from the pinned project repository.
// It compiles and tests in an isolated Git worktree before changing live files.
func (u *UpdateManager) Apply() error {
	u.mu.Lock()
	if u.state.Busy {
		u.mu.Unlock()
		return errors.New("更新正在执行中")
	}
	if !u.state.CanUpdate || u.state.Status != "available" {
		u.mu.Unlock()
		return errors.New("请先检查更新，确认确有可用的新版本")
	}
	target := u.state.LatestCommit
	u.state.Busy = true
	u.state.CanUpdate = false
	u.state.Status = "updating"
	u.state.Phase = "starting"
	u.state.Message = "正在准备安全更新…"
	u.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err := u.apply(ctx, target)
		if err != nil {
			u.setState(func(s *UpdateSnapshot) {
				s.Busy = false
				s.Status = "error"
				s.Phase = "error"
				s.Message = err.Error()
			})
			return
		}
		u.setState(func(s *UpdateSnapshot) {
			s.Status = "restarting"
			s.Phase = "restarting"
			s.Message = "更新完成，正在重新启动网页和 Agent API…"
		})
		time.Sleep(500 * time.Millisecond)
		select {
		case u.control <- lifecycleUpgrade:
		case <-time.After(5 * time.Second):
			u.setState(func(s *UpdateSnapshot) {
				s.Busy = false
				s.Status = "error"
				s.Message = "新版已安装，但未能自动重启，请重新打开 start.command"
			})
		}
	}()
	return nil
}

func (u *UpdateManager) apply(ctx context.Context, target string) error {
	if err := u.checkSource(ctx); err != nil {
		return err
	}
	u.setPhase("fetch", "正在从 GitHub 下载最新提交…")
	if _, err := gitCmd(ctx, u.repoDir, "fetch", "--no-tags", "origin", "main"); err != nil {
		return fmt.Errorf("下载更新失败：%w", err)
	}
	fetched, err := gitCmd(ctx, u.repoDir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if fetched != target {
		return errors.New("GitHub 在更新过程中出现新提交，请重新检查版本")
	}
	if _, err := gitCmd(ctx, u.repoDir, "merge-base", "--is-ancestor", u.state.LocalCommit, target); err != nil {
		return errors.New("新版本不是本地提交的快进版本，已取消更新")
	}
	tmp, err := os.MkdirTemp("", "siwc-upgrade-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	worktree := filepath.Join(tmp, "source")
	u.setPhase("testing", "正在隔离目录编译并测试新版本…")
	if _, err := gitCmd(ctx, u.repoDir, "worktree", "add", "--detach", worktree, target); err != nil {
		return fmt.Errorf("无法准备更新源码：%w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = gitCmd(cleanupCtx, u.repoDir, "worktree", "remove", "--force", worktree)
	}()
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = worktree
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("新版本测试失败，未替换旧程序：%s (%v)", truncateErr(b), err)
	}
	staged := filepath.Join(u.repoDir, ".siwc-provider.new")
	defer os.Remove(staged)
	if err := os.Remove(staged); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot remove old staging file: %w", err)
	}
	cmd = exec.CommandContext(ctx, "go", "build", "-ldflags=-X main.buildCommit="+target, "-o", staged, ".")
	cmd.Dir = worktree
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("新版本编译失败，未替换旧程序：%s (%v)", truncateErr(b), err)
	}
	if _, err := os.Stat(staged); err != nil {
		return err
	}
	if err := u.checkSource(ctx); err != nil {
		return fmt.Errorf("更新前源代码状态改变：%w", err)
	}
	u.setPhase("installing", "测试通过，正在快进仓库并安装新版…")
	if _, err := gitCmd(ctx, u.repoDir, "merge", "--ff-only", target); err != nil {
		return fmt.Errorf("无法安全更新仓库：%w", err)
	}
	backup := u.binaryPath + ".previous"
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(u.binaryPath, backup); err != nil {
		return fmt.Errorf("无法备份原程序：%w", err)
	}
	if err := os.Rename(staged, u.binaryPath); err != nil {
		_ = os.Rename(backup, u.binaryPath)
		return fmt.Errorf("无法安装新版程序（已尝试恢复旧版）：%w", err)
	}
	return nil
}

func truncateErr(b []byte) string {
	t := strings.TrimSpace(string(b))
	if len(t) > 500 {
		return t[:500]
	}
	return t
}
