package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// checkUpdateCollisions examines paths added by the target commit, including
// file/directory collisions. Unrelated untracked files are always permitted.
// Lstat catches ignored files and symlinks that git status might overlook.
// Run both when checking an update and immediately before installation.
func (u *UpdateManager) checkUpdateCollisions(ctx context.Context, target string) error {
	if !commitIDPattern.MatchString(target) || !commitIDPattern.MatchString(u.state.LocalCommit) {
		return errors.New("无法检查新版本文件冲突：提交号无效")
	}
	result, err := gitCmd(ctx, u.repoDir, "diff", "--no-renames", "--diff-filter=A", "--name-only", "-z", u.state.LocalCommit, target, "--")
	if err != nil {
		return fmt.Errorf("无法检查新版本新增文件：%w", err)
	}
	conflicts := make(map[string]struct{})
	for _, path := range strings.Split(result, "\x00") {
		if path == "" {
			continue
		}
		rel := filepath.FromSlash(path)
		if !filepath.IsLocal(rel) {
			return errors.New("Git 返回了不安全的新增文件路径")
		}
		for current := rel; current != "." && current != string(filepath.Separator); current = filepath.Dir(current) {
			info, err := os.Lstat(filepath.Join(u.repoDir, current))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("检查文件路径 %q 失败：%w", filepath.ToSlash(current), err)
			}
			// Added upstream files must not replace an existing local item;
			// any existing parent must be a real directory, not a file/symlink.
			if current == rel || !info.IsDir() {
				conflicts[filepath.ToSlash(current)] = struct{}{}
			}
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	paths := make([]string, 0, len(conflicts))
	for path := range conflicts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	const limit = 8
	visible := paths
	if len(visible) > limit {
		visible = visible[:limit]
	}
	message := "本地文件与新版本新增路径冲突，已停止更新且未覆盖这些文件：" + strings.Join(visible, "、")
	if len(paths) > limit {
		message += fmt.Sprintf("（另有 %d 个冲突路径）", len(paths)-limit)
	}
	return errors.New(message)
}
