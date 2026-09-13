package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai-education/backend/internal/docker"
)

// ErrNothingToCommit is returned by CommitWorkspace when `git commit` finds
// nothing staged (no files changed since the last commit).
var ErrNothingToCommit = errors.New("コミットする変更がありません")

// ErrCommitNotFound is returned by GitDiff/RevertWorkspaceTo when the given
// hash doesn't resolve to a commit in the workspace's repo (typo, or a
// container whose git history doesn't contain it).
var ErrCommitNotFound = errors.New("指定されたコミットが見つかりません")

// ErrNothingToRevert is returned by RevertWorkspaceTo when the workspace
// already matches the target commit's snapshot exactly (nothing to restore).
var ErrNothingToRevert = errors.New("既にこの状態です")

// GitCommit is one row of `git log`(「変更履歴」タブのタイムライン一覧)。
type GitCommit struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Message string `json:"message"`
	Date    string `json:"date"`
}

// GitDiffFile is one changed path within a single commit, as reported by
// `git show --name-status`.
type GitDiffFile struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "added" | "modified" | "deleted" | "renamed" | "copied"
}

// GitStatusEntry is one currently uncommitted change, as reported by
// `git status --porcelain=v1`(「ソース管理」パネルの変更ファイル一覧)。
type GitStatusEntry struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "added" | "modified" | "deleted" | "renamed" | "copied" | "untracked"
}

// GitCommitDiff is the response for GET /sandbox/git/diff/:hash: the
// commit itself, every path it touched, and the before/after full text of
// one selected file (SelectedPath) - ready to feed straight into Monaco's
// DiffEditor(original/modified props)without the frontend having to parse a
// unified diff itself.
type GitCommitDiff struct {
	Commit       GitCommit     `json:"commit"`
	Files        []GitDiffFile `json:"files"`
	SelectedPath string        `json:"selected_path"`
	Original     string        `json:"original"`
	Modified     string        `json:"modified"`
}

// gitLogFormat produces one `|`-joined line per commit that parseCommitLine
// below can split back apart. %h(短縮ハッシュ)は`git show`/`git checkout`
// 等どの参照コマンドにもそのまま渡せる。
const gitLogFormat = "%h|%an|%s|%ad"

// parseCommitLine splits one gitLogFormat line back into a GitCommit.
// SplitN(..., 4) keeps the message field intact even if the commit subject
// itself happens to contain "|" - only the first 3 delimiters are treated as
// field separators, so any extra "|" stays inside message rather than
// shifting date out of place.
func parseCommitLine(line string) (GitCommit, bool) {
	parts := strings.SplitN(line, "|", 4)
	if len(parts) != 4 {
		return GitCommit{}, false
	}
	return GitCommit{Hash: parts[0], Author: parts[1], Message: parts[2], Date: parts[3]}, true
}

// parseNameStatus parses `git show --name-status --pretty=format:`'s output
// (one "<code>\t<path>" line per changed file; renames/copies add a third
// tab-separated field, the destination path, which we take as Path).
func parseNameStatus(output string) []GitDiffFile {
	var files []GitDiffFile
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		code := parts[0]
		filePath := parts[len(parts)-1]
		status := "modified"
		switch code[0] {
		case 'A':
			status = "added"
		case 'D':
			status = "deleted"
		case 'R':
			status = "renamed"
		case 'C':
			status = "copied"
		}
		files = append(files, GitDiffFile{Path: filePath, Status: status})
	}
	return files
}

// parseGitStatusPorcelain parses `git status --porcelain=v1`'s output(1行
// あたり「2文字のステータスコード + 空白 + パス」、リネームは
// "旧パス -> 新パス"の形)。このアプリのコミット処理(CommitWorkspace)は常に
// `git add -A`してから`git commit`するため、呼び出し側にとってはステージ済み
// /未ステージの違いを区別する意味が薄い - 2文字のどちらか一方にでも
// 該当する記号があれば、その種別として1行にまとめる。
func parseGitStatusPorcelain(output string) []GitStatusEntry {
	var entries []GitStatusEntry
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		code := line[:2]
		filePath := line[3:]
		if idx := strings.Index(filePath, " -> "); idx != -1 {
			filePath = filePath[idx+4:]
		}
		status := "modified"
		switch {
		case code == "??":
			status = "untracked"
		case strings.Contains(code, "A"):
			status = "added"
		case strings.Contains(code, "D"):
			status = "deleted"
		case strings.Contains(code, "R"):
			status = "renamed"
		case strings.Contains(code, "C"):
			status = "copied"
		}
		entries = append(entries, GitStatusEntry{Path: filePath, Status: status})
	}
	return entries
}

// GitStatus lists the workspace's currently uncommitted changes(「ソース
// 管理」パネルの変更ファイル一覧) - 講師・生徒どちらの画面から呼ばれても
// 同じコンテナのworkspaceへ都度問い合わせるだけで、専用の同期処理は無い
// (Gitの状態自体がコンテナ上に1つしか無いため、自然に共有される。
// authorizeSandboxCourseのas_user上書き、internal/handler/file.go参照)。
func GitStatus(ctx context.Context, cli docker.ContainerAPI, containerID string) ([]GitStatusEntry, error) {
	stdout, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{
		"git", "-C", sandboxWorkspacePath, "status", "--porcelain=v1",
	})
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("git status failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
	}
	entries := parseGitStatusPorcelain(stdout)
	if entries == nil {
		entries = []GitStatusEntry{}
	}
	return entries, nil
}

// gitShowFileAtRef returns the content of ref(a "<commit>:<path>"-style
// revision spec)- exists=falseが返るのは、そのパスがそのコミット時点に
// 存在しない場合(新規追加ファイルのoriginal、削除ファイルのmodified等)で、
// これはエラーではなく「空」として扱う正常系。
func gitShowFileAtRef(ctx context.Context, cli docker.ContainerAPI, containerID, ref string) (content string, exists bool, err error) {
	stdout, _, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{"git", "-C", sandboxWorkspacePath, "show", ref})
	if err != nil {
		return "", false, err
	}
	if exitCode != 0 {
		return "", false, nil
	}
	return stdout, true, nil
}

// CommitWorkspace runs `git add -A && git commit -m <message>` inside the
// sandbox's workspace(「保存(コミット)」ボタン、NextPlan.md フェーズ5)。
// git initはコンテナ起動時にentrypoint.shが自動実行済み
// (sandbox-images/sandbox-base/entrypoint.sh)なので、ここでは前提として扱う。
// messageはargvの1要素としてそのままgitプロセスへ渡る(runContainerCommand
// 参照 - シェル文字列に埋め込まないため引用符・バックティック等のエスケープは
// 不要)。
func CommitWorkspace(ctx context.Context, cli docker.ContainerAPI, containerID, message string) error {
	if _, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{"git", "-C", sandboxWorkspacePath, "add", "-A"}); err != nil {
		return err
	} else if exitCode != 0 {
		return fmt.Errorf("git add failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
	}

	stdout, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{"git", "-C", sandboxWorkspacePath, "commit", "-m", message})
	if err != nil {
		return err
	}
	if exitCode != 0 {
		// 「変更なし」はgit commit独自の標準出力メッセージ(標準エラーではない、
		// 検証済み)。生徒向けには通信エラーと区別して案内する。
		if strings.Contains(stdout, "nothing to commit") {
			return ErrNothingToCommit
		}
		return fmt.Errorf("git commit failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr+stdout))
	}
	return nil
}

// GitHistory lists every commit in the workspace's repo, newest first
// (「変更履歴」タブのタイムライン一覧)。A repo with no commits yet(生徒が
// まだ一度も保存していない場合)は空リストとして扱う - エラーではない。
func GitHistory(ctx context.Context, cli docker.ContainerAPI, containerID string) ([]GitCommit, error) {
	stdout, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{
		"git", "-C", sandboxWorkspacePath, "log", "--pretty=format:" + gitLogFormat, "--date=iso",
	})
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		if strings.Contains(stderr, "does not have any commits yet") || strings.Contains(stderr, "unknown revision") {
			return []GitCommit{}, nil
		}
		return nil, fmt.Errorf("git log failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
	}

	commits := []GitCommit{}
	for _, line := range strings.Split(stdout, "\n") {
		if line == "" {
			continue
		}
		if commit, ok := parseCommitLine(line); ok {
			commits = append(commits, commit)
		}
	}
	return commits, nil
}

// GitDiff resolves one commit's metadata, its full changed-file list, and the
// before/after content of selectedPath within it (defaulting to the first
// changed file when selectedPath is blank) - everything DiffViewer needs to
// drive Monaco's DiffEditor for that file without a second round-trip.
func GitDiff(ctx context.Context, cli docker.ContainerAPI, containerID, hash, selectedPath string) (GitCommitDiff, error) {
	metaOut, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{
		"git", "-C", sandboxWorkspacePath, "show", "-s", "--pretty=format:" + gitLogFormat, "--date=iso", hash,
	})
	if err != nil {
		return GitCommitDiff{}, err
	}
	if exitCode != 0 {
		if strings.Contains(stderr, "unknown revision") || strings.Contains(stderr, "bad revision") || strings.Contains(stderr, "bad object") {
			return GitCommitDiff{}, ErrCommitNotFound
		}
		return GitCommitDiff{}, fmt.Errorf("git show failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
	}
	commit, ok := parseCommitLine(strings.TrimSpace(metaOut))
	if !ok {
		return GitCommitDiff{}, ErrCommitNotFound
	}

	nameStatusOut, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{
		"git", "-C", sandboxWorkspacePath, "show", "--name-status", "--pretty=format:", hash,
	})
	if err != nil {
		return GitCommitDiff{}, err
	}
	if exitCode != 0 {
		return GitCommitDiff{}, fmt.Errorf("git show --name-status failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
	}
	files := parseNameStatus(nameStatusOut)

	target := selectedPath
	if target == "" && len(files) > 0 {
		target = files[0].Path
	}

	var original, modified string
	if target != "" {
		modified, _, err = gitShowFileAtRef(ctx, cli, containerID, hash+":"+target)
		if err != nil {
			return GitCommitDiff{}, err
		}
		original, _, err = gitShowFileAtRef(ctx, cli, containerID, hash+"~1:"+target)
		if err != nil {
			return GitCommitDiff{}, err
		}
	}

	return GitCommitDiff{
		Commit:       commit,
		Files:        files,
		SelectedPath: target,
		Original:     original,
		Modified:     modified,
	}, nil
}

// RevertWorkspaceTo restores the workspace's tracked files to exactly match
// hash's snapshot, recorded as a brand-new commit on top of the current
// history(「この状態に巻き戻す」ボタン)。既存コミットは一切書き換え/削除
// しない(git reset --hardのような破壊的な方法を避けている) - 生徒が変更履歴
// タブで行き来しても、巻き戻す前の状態へまた戻れるようにするため。
// 1) 対象ハッシュの存在を先に検証してから2)破壊的な操作(rm -rf)に入るため、
// 不正なハッシュでworkspaceを空にしてしまうことはない。
func RevertWorkspaceTo(ctx context.Context, cli docker.ContainerAPI, containerID, hash string) error {
	if _, _, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{
		"git", "-C", sandboxWorkspacePath, "cat-file", "-e", hash + "^{commit}",
	}); err != nil {
		return err
	} else if exitCode != 0 {
		return ErrCommitNotFound
	}

	// git rm -rf --ignore-unmatch .: 現在追跡中の全ファイルをインデックス/
	// ワークツリーから削除(まだ何もコミットされていなくても--ignore-unmatch
	// で正常終了)。続くcheckoutが対象ハッシュに存在しないパス(その後に
	// 追加されたファイル)を削除しきれない問題を避けるための前処理。
	script := `set -e
cd "$1"
git rm -rf -q --ignore-unmatch .
git checkout "$2" -- .
git add -A
git commit -m "以前の状態に戻す ($2)"
`
	stdout, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{"sh", "-c", script, "sh", sandboxWorkspacePath, hash})
	if err != nil {
		return err
	}
	if exitCode != 0 {
		if strings.Contains(stdout, "nothing to commit") {
			return ErrNothingToRevert
		}
		return fmt.Errorf("revert failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr+stdout))
	}
	return nil
}
