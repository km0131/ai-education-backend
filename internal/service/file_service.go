package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"ai-education/backend/internal/docker"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// sandboxFileSizeLimit caps how much file content this API will read from or
// write to the container in one call - large logs/data files would
// otherwise be loaded entirely into Go process memory and into Monaco
// Editor, which isn't what a source code editor is for. GetSandboxFileContent
// reads one byte past this limit so it can tell "exactly at the limit" apart
// from "longer than the limit"; WriteSandboxFileContent checks it up front.
const sandboxFileSizeLimit = 1_000_000 // 1MB

var (
	// ErrSandboxPathInvalid: pathがworkspace配下の絶対パスでない場合。
	ErrSandboxPathInvalid = errors.New("パスが不正です")
	// ErrSandboxPathNotFound: 指定されたファイル/ディレクトリが存在しない場合。
	ErrSandboxPathNotFound = errors.New("指定されたパスが見つかりません")
	// ErrSandboxPathIsDirectory: ファイル内容取得でディレクトリを指定した場合。
	ErrSandboxPathIsDirectory = errors.New("指定されたパスはディレクトリです")
	// ErrSandboxFileTooLarge: ファイルサイズがsandboxFileSizeLimitを超える場合。
	ErrSandboxFileTooLarge = errors.New("ファイルサイズが大きすぎます")
	// ErrSandboxFileNotText: NULバイトを含む(バイナリと推定される)場合。
	ErrSandboxFileNotText = errors.New("テキストファイルではありません")
	// ErrSandboxPathAlreadyExists: 作成/移動先に既に何か存在する場合。
	ErrSandboxPathAlreadyExists = errors.New("既に存在します")
)

// IsSandboxRoot reports whether resolvedPath (already produced by
// ResolveSandboxPath) is exactly the workspace root itself, rather than an
// entry inside it. Handlers use this to refuse deleting/moving the root
// directory itself - it's the sandbox's bind-mounted disk-quota pool slot
// (program_service.go), not a regular tree entry.
func IsSandboxRoot(resolvedPath string) bool {
	return resolvedPath == sandboxWorkspacePath
}

// Sentinel exit codes for the shell snippets in CreateSandboxPath/
// MoveSandboxPath/DeleteSandboxPath below, chosen not to collide with the
// wrapped commands' own exit codes, so Go can distinguish "the thing we
// explicitly checked for" from any other failure without parsing
// locale-dependent stderr text for that specific case.
const (
	exitDestinationExists = 3
	exitPathMissing       = 4
)

// SandboxFileEntry is one row of a directory listing.
type SandboxFileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// ResolveSandboxPath normalizes a client-supplied path and confines it to
// sandboxWorkspacePath (/root/workspace) - the only directory this API is
// meant to expose, matching the bind-mounted disk-quota pool slot each
// sandbox gets (program_service.go). Blank input defaults to the workspace
// root itself. Uses lexical path.Clean (POSIX rules, matching the Linux
// container filesystem) so "../"-style escapes collapse before the prefix
// check runs, rather than being passed through to the container as-is.
func ResolveSandboxPath(requested string) (string, error) {
	if requested == "" {
		return sandboxWorkspacePath, nil
	}
	cleaned := path.Clean(requested)
	if !path.IsAbs(cleaned) {
		return "", ErrSandboxPathInvalid
	}
	if cleaned != sandboxWorkspacePath && !strings.HasPrefix(cleaned, sandboxWorkspacePath+"/") {
		return "", ErrSandboxPathInvalid
	}
	return cleaned, nil
}

// runContainerCommand execs cmd (argv form - never a shell string, so a
// caller-controlled value like a file path can't be interpreted as shell
// syntax) inside containerID and captures stdout/stderr/exit code. Uses a
// non-TTY attach (unlike the interactive shell relay in shell_service.go) so
// stdout/stderr come back demultiplexed via stdcopy instead of merged into
// one stream.
func runContainerCommand(ctx context.Context, cli docker.ContainerAPI, containerID string, cmd []string) (stdout string, stderr string, exitCode int, err error) {
	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	})
	if err != nil {
		return "", "", 0, err
	}

	hijacked, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		return "", "", 0, err
	}
	defer hijacked.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdoutBuf, &stderrBuf, hijacked.Reader); err != nil {
		return "", "", 0, err
	}

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", "", 0, err
	}

	return stdoutBuf.String(), stderrBuf.String(), inspect.ExitCode, nil
}

// runContainerCommandWithStdin is like runContainerCommand but also feeds
// stdin bytes to the exec'd process before reading its output - used by
// WriteSandboxFileContent to stream a file's new content in without ever
// passing it through a shell (so its size/content need no escaping at all,
// unlike the small fixed path arguments the other Create/Move/Delete
// snippets pass via $1/$2).
func runContainerCommandWithStdin(ctx context.Context, cli docker.ContainerAPI, containerID string, cmd []string, stdin []byte) (stdout string, stderr string, exitCode int, err error) {
	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	})
	if err != nil {
		return "", "", 0, err
	}

	hijacked, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		return "", "", 0, err
	}
	defer hijacked.Close()

	if _, err := hijacked.Conn.Write(stdin); err != nil {
		return "", "", 0, err
	}
	// stdinの終端(EOF)を明示的に伝える半クローズ。これをしないと、stdinから
	// 読み切るまで待つコマンド(今回のdd等)がいつまでも終了しない。
	if closeWriter, ok := hijacked.Conn.(interface{ CloseWrite() error }); ok {
		if err := closeWriter.CloseWrite(); err != nil {
			return "", "", 0, err
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdoutBuf, &stderrBuf, hijacked.Reader); err != nil {
		return "", "", 0, err
	}

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", "", 0, err
	}

	return stdoutBuf.String(), stderrBuf.String(), inspect.ExitCode, nil
}

// ListSandboxFiles lists the immediate children of dirPath (already
// validated by ResolveSandboxPath) inside containerID - one level only, not
// recursive, so the frontend tree can lazily expand folders instead of
// pulling the whole tree (which could be huge: venvs, node_modules, .git...)
// on every open.
func ListSandboxFiles(ctx context.Context, cli docker.ContainerAPI, containerID, dirPath string) ([]SandboxFileEntry, error) {
	stdout, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{
		"find", dirPath, "-mindepth", "1", "-maxdepth", "1", "-printf", "%y\t%s\t%f\n",
	})
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		if strings.Contains(stderr, "No such file or directory") {
			return nil, ErrSandboxPathNotFound
		}
		return nil, fmt.Errorf("find failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
	}

	var entries []SandboxFileEntry
	for _, line := range strings.Split(stdout, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		typeChar, sizeStr, name := parts[0], parts[1], parts[2]
		size, _ := strconv.ParseInt(sizeStr, 10, 64)
		entries = append(entries, SandboxFileEntry{
			Name:  name,
			Path:  path.Join(dirPath, name),
			IsDir: typeChar == "d",
			Size:  size,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir // directories first, matching VS Code's explorer ordering
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// GetSandboxFileContent reads a single text file's content (already
// path-validated by ResolveSandboxPath). Refuses directories, files over
// sandboxFileSizeLimit, and anything containing a NUL byte (treated as
// binary) - none of those belong in a Monaco Editor text buffer.
func GetSandboxFileContent(ctx context.Context, cli docker.ContainerAPI, containerID, filePath string) (string, error) {
	// head -c N+1 lets us tell "file is exactly N bytes" apart from "file is
	// longer than N bytes" without a separate stat round-trip.
	stdout, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{
		"head", "-c", strconv.Itoa(sandboxFileSizeLimit + 1), "--", filePath,
	})
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		switch {
		case strings.Contains(stderr, "No such file or directory"):
			return "", ErrSandboxPathNotFound
		case strings.Contains(stderr, "Is a directory"):
			return "", ErrSandboxPathIsDirectory
		default:
			return "", fmt.Errorf("head failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
		}
	}
	if len(stdout) > sandboxFileSizeLimit {
		return "", ErrSandboxFileTooLarge
	}
	if strings.ContainsRune(stdout, '\x00') {
		return "", ErrSandboxFileNotText
	}
	return stdout, nil
}

// ResolveSandboxRelativePath is like ResolveSandboxPath, but requestedRelative
// is already relative to the workspace root(先頭の"/"はあってもなくてもよい)
// instead of needing to be pre-prefixed with sandboxWorkspacePath. Used by
// the Web preview の静的ファイル配信(PreviewFile、preview_handler.go)で、
// リクエストURLのパス部分がそのままワークスペース相対パスになっているため。
// path.Cleanでの正規化により、"../"を使った脱出は(ルート直下に丸められる
// だけで)ワークスペース外には出られない。
func ResolveSandboxRelativePath(requestedRelative string) (string, error) {
	cleaned := path.Clean("/" + requestedRelative)
	return ResolveSandboxPath(sandboxWorkspacePath + cleaned)
}

// ReadSandboxFileBytes reads filePath's raw bytes(already path-validated by
// ResolveSandboxRelativePath) - unlike GetSandboxFileContent, it does not
// reject binary content(NUL bytes)since Webプレビューの静的ファイル配信
// (preview_handler.go)は画像等のバイナリアセットもそのまま返す必要がある。
// サイズ上限(sandboxFileSizeLimit)はMonaco Editor用の既存関数と同じ値を
// 流用する。
func ReadSandboxFileBytes(ctx context.Context, cli docker.ContainerAPI, containerID, filePath string) ([]byte, error) {
	stdout, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{
		"head", "-c", strconv.Itoa(sandboxFileSizeLimit + 1), "--", filePath,
	})
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		switch {
		case strings.Contains(stderr, "No such file or directory"):
			return nil, ErrSandboxPathNotFound
		case strings.Contains(stderr, "Is a directory"):
			return nil, ErrSandboxPathIsDirectory
		default:
			return nil, fmt.Errorf("head failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
		}
	}
	if len(stdout) > sandboxFileSizeLimit {
		return nil, ErrSandboxFileTooLarge
	}
	return []byte(stdout), nil
}

// CreateSandboxPath creates a new, empty file or directory at targetPath
// (already validated by ResolveSandboxPath). Like "New File"/"New Folder" in
// most editors, it never overwrites - ErrSandboxPathAlreadyExists if
// something is already there, ErrSandboxPathNotFound if the parent directory
// doesn't exist yet.
func CreateSandboxPath(ctx context.Context, cli docker.ContainerAPI, containerID, targetPath string, isDir bool) error {
	var cmd []string
	if isDir {
		cmd = []string{"mkdir", "--", targetPath}
	} else {
		// `set -C` (noclobber) makes `>` fail instead of truncating if
		// targetPath already exists. targetPath is passed as $1 (argv), never
		// interpolated into the script text, so a path containing shell
		// metacharacters can't be read as syntax.
		cmd = []string{"sh", "-c", `set -C; : > "$1"`, "sh", targetPath}
	}

	_, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, cmd)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		switch {
		case strings.Contains(stderr, "File exists"), strings.Contains(stderr, "cannot overwrite"):
			return ErrSandboxPathAlreadyExists
		case strings.Contains(stderr, "No such file or directory"):
			return ErrSandboxPathNotFound
		default:
			return fmt.Errorf("create failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
		}
	}
	return nil
}

// MoveSandboxPath renames/moves oldPath to newPath (both already validated
// by ResolveSandboxPath) - the same operation covers both "rename" and
// "move" in a file explorer UI. mv -n silently no-ops instead of failing
// when the destination already exists, so the conflict is checked
// explicitly first (exitDestinationExists) to report it as an error.
func MoveSandboxPath(ctx context.Context, cli docker.ContainerAPI, containerID, oldPath, newPath string) error {
	cmd := []string{
		"sh", "-c",
		`if [ -e "$2" ]; then exit ` + strconv.Itoa(exitDestinationExists) + `; fi; exec mv -- "$1" "$2"`,
		"sh", oldPath, newPath,
	}

	_, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, cmd)
	if err != nil {
		return err
	}
	if exitCode == exitDestinationExists {
		return ErrSandboxPathAlreadyExists
	}
	if exitCode != 0 {
		if strings.Contains(stderr, "No such file or directory") {
			return ErrSandboxPathNotFound
		}
		return fmt.Errorf("move failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
	}
	return nil
}

// DeleteSandboxPath removes a file, or a directory and everything under it
// (already validated by ResolveSandboxPath). Existence is checked explicitly
// first (exitPathMissing) so a missing path is reported as
// ErrSandboxPathNotFound instead of rm -rf's default silent no-op.
func DeleteSandboxPath(ctx context.Context, cli docker.ContainerAPI, containerID, targetPath string) error {
	cmd := []string{
		"sh", "-c",
		`if [ ! -e "$1" ]; then exit ` + strconv.Itoa(exitPathMissing) + `; fi; exec rm -rf -- "$1"`,
		"sh", targetPath,
	}

	_, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, cmd)
	if err != nil {
		return err
	}
	if exitCode == exitPathMissing {
		return ErrSandboxPathNotFound
	}
	if exitCode != 0 {
		return fmt.Errorf("delete failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
	}
	return nil
}

// WriteSandboxFileContent overwrites filePath (already validated by
// ResolveSandboxPath) with content, creating it if it doesn't exist yet -
// this is Monaco Editor's "save", so it behaves like a normal text editor
// save (works whether or not the file was there already), unlike
// CreateSandboxPath's deliberately non-overwriting "New File".
//
// Streams content to `dd`'s stdin instead of ever building a shell command
// string out of it: content is arbitrary, editor-sized text (up to
// sandboxFileSizeLimit) that would otherwise need shell-escaping identical
// to what the argv-only commands elsewhere in this file are specifically
// designed to avoid for path arguments. `dd of=<path>` (not `cat >`) is used
// so the destination is also an ordinary argv value, not shell redirection
// syntax that would require a shell to interpret.
func WriteSandboxFileContent(ctx context.Context, cli docker.ContainerAPI, containerID, filePath, content string) error {
	if len(content) > sandboxFileSizeLimit {
		return ErrSandboxFileTooLarge
	}

	_, stderr, exitCode, err := runContainerCommandWithStdin(ctx, cli, containerID, []string{
		"dd", "of=" + filePath, "status=none",
	}, []byte(content))
	if err != nil {
		return err
	}
	if exitCode != 0 {
		switch {
		case strings.Contains(stderr, "Is a directory"):
			return ErrSandboxPathIsDirectory
		case strings.Contains(stderr, "No such file or directory"):
			return ErrSandboxPathNotFound
		default:
			return fmt.Errorf("write failed (exit=%d): %s", exitCode, strings.TrimSpace(stderr))
		}
	}
	return nil
}
