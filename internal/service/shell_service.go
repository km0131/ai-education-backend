package service

import (
	"bufio"
	"context"
	"errors"
	"net"

	"ai-education/backend/internal/docker"

	"github.com/docker/docker/api/types/container"
	"github.com/google/uuid"
)

// ErrSandboxNotRunning is returned when a shell/LSP session is requested for
// a sandbox that exists but isn't currently running - the student must
// resume it first (ResumeProgramContainer) before a WebSocket relay can
// attach to it.
var ErrSandboxNotRunning = errors.New("サンドボックスが起動していません")

// sandboxShellCmd is the interactive shell exec'd inside the sandbox for the
// WebSocket relay. sandbox-base is Ubuntu-based (see
// sandbox-images/sandbox-base/Dockerfile), which always ships /bin/bash.
var sandboxShellCmd = []string{"bash"}

// ContainerIDForSession resolves the current running Docker container ID for
// this (user, course) pair, for handlers that need direct docker exec access
// (シェル/LSP WebSocket中継、NextPlan.md フェーズ3・6)。Returns
// ErrSandboxNotFound if no sandbox record exists at all, or
// ErrSandboxNotRunning if one exists but is stopped.
func ContainerIDForSession(ctx context.Context, cli docker.ContainerAPI, userID uuid.UUID, courseID uint) (string, error) {
	existing, err := findSandbox(ctx, cli, userID, courseID)
	if err != nil {
		return "", err
	}
	if existing == nil {
		return "", ErrSandboxNotFound
	}
	if existing.State != "running" {
		return "", ErrSandboxNotRunning
	}
	return existing.ID, nil
}

// ShellSession is an attached, interactive (TTY) exec session ready for
// bidirectional byte streaming. It deliberately exposes only net.Conn/
// bufio.Reader (not the Docker SDK's HijackedResponse type directly) so the
// WebSocket relay loop (internal/handler/shell_handler.go) only has to move
// bytes, decoupled from the Docker SDK.
type ShellSession struct {
	ExecID string
	Conn   net.Conn      // Write() here = the container process's stdin
	Reader *bufio.Reader // Read() here = the container process's stdout+stderr (merged, since Tty:true)
}

// Resize updates the exec session's TTY size (called when the browser's
// xterm.js viewport is resized).
func (s *ShellSession) Resize(ctx context.Context, cli docker.ContainerAPI, cols, rows uint) error {
	return cli.ContainerExecResize(ctx, s.ExecID, container.ResizeOptions{Width: cols, Height: rows})
}

// Close releases the underlying hijacked connection. Safe to call more than
// once (net.Conn.Close() on an already-closed conn just returns an error,
// which callers here intentionally ignore).
func (s *ShellSession) Close() {
	_ = s.Conn.Close()
}

// StartShellSession creates an interactive shell exec in containerID and
// attaches to it, ready for the caller to stream bytes both ways
// (NextPlan.md フェーズ3「コンテナ内プロセスへのexecをGoから起動し、標準入出力を
// WebSocketにストリーミング」)。cols/rows set the initial TTY size; the
// caller should follow up with Resize() once it knows the browser's actual
// terminal size.
//
// WorkingDir=sandboxWorkspacePathを明示しないと、execはイメージのWORKDIR
// (Dockerfileの`WORKDIR /root`)から始まってしまい、ファイルツリー/エディタ
// が表示している「プロジェクトルート」(/root/workspace)と食い違う - 生徒が
// ターミナルで相対パスを打つたびに迷子になり、「実行」ボタン(EditorPane/
// runCommand.ts)がプロジェクトルート相対で組み立てたコマンドも見つからない
// ファイルとして失敗する。StartLspSession(lsp_service.go)と同じ理由で
// 同じ値を明示している。
func StartShellSession(ctx context.Context, cli docker.ContainerAPI, containerID string, cols, rows uint) (*ShellSession, error) {
	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          sandboxShellCmd,
		WorkingDir:   sandboxWorkspacePath,
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		ConsoleSize:  &[2]uint{rows, cols},
		Env:          []string{"TERM=xterm-256color"},
	})
	if err != nil {
		return nil, err
	}

	hijacked, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return nil, err
	}

	return &ShellSession{ExecID: execResp.ID, Conn: hijacked.Conn, Reader: hijacked.Reader}, nil
}
