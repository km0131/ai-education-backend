package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-education/backend/internal/docker"

	"github.com/docker/docker/api/types/container"
)

// lspConfigPath is where the拡張子→起動コマンド対応表を置く場所
// (ワークスペースルート相対の.ai/lsp-config.json)。entrypoint.shが初回
// 起動時にデフォルト({"py": "pyright-langserver --stdio"})を書き込み
// 済み(sandbox-base/Dockerfile参照)なので、Pythonは生徒が何もしなくても
// 自動でLSPが起動する。他言語を使う場合のみ、生徒がこのファイルに追記する
// (新規UIパネル不要、既存のファイル編集機能で完結、NextPlan.md フェーズ6)。
const lspConfigPath = sandboxWorkspacePath + "/.ai/lsp-config.json"

// ErrLspLanguageNotConfigured is returned when the requested file's
// extension has no matching entry in .ai/lsp-config.json (拡張子ぶんの
// 言語サーバーがまだ設定されていない - エラーではなく「その言語では補完を
// 提供しない」という正常な状態として扱う)。
var ErrLspLanguageNotConfigured = errors.New("この拡張子に対応する言語サーバーが設定されていません")

// ErrLspConcurrencyLimitReached is returned by StartLspSession when the
// server全体で同時に起動できるLSPプロセス数(lspMaxConcurrent、言語種別を
// 問わず)が既に上限に達している場合(NextPlan.md フェーズ6「同時起動数の
// 上限制御」)。待機列には入れず、素直にエラーとして返す方針
// (ErrSandboxPoolExhaustedと同じ考え方 - キューイングは公平性/キャンセル
// 処理等の複雑さの割に、教育用サンドボックスの規模では過剰)。
var ErrLspConcurrencyLimitReached = errors.New("LSPサーバーが混み合っています。しばらくしてから再度お試しください")

const (
	// defaultLspMaxConcurrent is how many LSP processes(言語種別を問わず、
	// サーバー全体で)may run at once when LSP_MAX_CONCURRENT is unset.
	defaultLspMaxConcurrent = 8
	// defaultLspIdleTimeoutMinutes: 「編集中のみ起動」(NextPlan.md フェーズ6)。
	// コンテナ/サンドボックス全体のアイドルタイムアウト(SANDBOX_IDLE_TIMEOUT_MINUTES、
	// program_service.go)とは別に、LSPプロセス単位でこの時間だけ入出力が
	// 無ければ自動停止する。
	defaultLspIdleTimeoutMinutes = 5
)

// lspMaxConcurrent reads LSP_MAX_CONCURRENT from the environment, falling
// back to defaultLspMaxConcurrent when unset or invalid - resourceLimits/
// idleTimeout(program_service.go)と同じ「.envで調整可能、具体的な数値は
// 実運用を踏まえて確定する未確定事項」という方針を踏襲する。
func lspMaxConcurrent() int {
	if v := os.Getenv("LSP_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultLspMaxConcurrent
}

// LspIdleTimeout reads LSP_IDLE_TIMEOUT_MINUTES from the environment,
// falling back to defaultLspIdleTimeoutMinutes when unset or invalid。
// relayLsp(handlerパッケージ側)のアイドル監視ゴルーチンが参照するため
// exportしている。
func LspIdleTimeout() time.Duration {
	minutes := defaultLspIdleTimeoutMinutes
	if v := os.Getenv("LSP_IDLE_TIMEOUT_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			minutes = n
		}
	}
	return time.Duration(minutes) * time.Minute
}

// lspConcurrencyMu/lspConcurrencyCount track how many LSP processes are
// currently running across the WHOLE server(全生徒・全言語種別の合計) -
// program_service.goのプールスロットと違い、DBに永続化する必要はない
// (プロセスの生死そのものと1:1で、プロセス終了=このプロセス内での
// カウンタ減算で必ず追従できるため)。
var (
	lspConcurrencyMu    sync.Mutex
	lspConcurrencyCount int
)

// tryAcquireLspSlot reserves one of the server-wide concurrent LSP process
// slots. Returns false if lspMaxConcurrent()has already been reached -
// caller must not start a process in that case. 成功した場合は必ず
// releaseLspSlotと対で呼ぶこと(LspSession.Closeがこれを保証する)。
func tryAcquireLspSlot() bool {
	lspConcurrencyMu.Lock()
	defer lspConcurrencyMu.Unlock()
	if lspConcurrencyCount >= lspMaxConcurrent() {
		return false
	}
	lspConcurrencyCount++
	return true
}

func releaseLspSlot() {
	lspConcurrencyMu.Lock()
	defer lspConcurrencyMu.Unlock()
	if lspConcurrencyCount > 0 {
		lspConcurrencyCount--
	}
}

// LspSession is an attached, non-TTY exec session for a language server
// process. ShellSession(shell_service.go)と違いTty:falseで起動する - LSPは
// 疑似端末ではなくプレーンなstdin/stdout上でJSON-RPC(Content-Length
// フレーミング)を話すプロトコルのため、TTYの行編集/エコーは不要かつ有害
// (プロトコルバイト列を壊してしまう)。
type LspSession struct {
	ExecID string
	Conn   net.Conn      // Write() here = 言語サーバーの標準入力
	Reader *bufio.Reader // Read() here = Dockerの多重化フレーム(標準出力/標準エラー混在、下記StdCopy参照)

	slotReleased sync.Once // tryAcquireLspSlotで確保した1枠を、Closeが何度呼ばれても確実に1回だけ返却する
}

// Close releases the underlying hijacked connection and returns this
// session's concurrency slot(tryAcquireLspSlot)。Safe to call more than once
// (net.Conn.Close()と同じ理由 - スロット返却もsync.Onceで一度だけ)。
func (s *LspSession) Close() {
	_ = s.Conn.Close()
	s.slotReleased.Do(releaseLspSlot)
}

// ExitCode reports the language server process's exit code, once it has
// already finished(LSPプロセスクラッシュ時のリトライ・ユーザー通知フロー、
// NextPlan.md フェーズ6) - relayLsp(lsp_handler.go)がexecの標準出力側を
// 読み切った(=プロセスが終了した)直後に呼び、それがアイドルタイムアウトや
// ブラウザ側の切断ではなく「プロセス自体が予期せず終了した」ケースかどうかを
// 判定するために使う。okがfalseの場合(inspectそのものの失敗、または
// まだRunning中 - タイミング上通常起こらないが念のため)は、判定不能として
// クラッシュ扱いにしない側(呼び出し元)の責務とする。
func (s *LspSession) ExitCode(ctx context.Context, cli docker.ContainerAPI) (code int, ok bool) {
	inspect, err := cli.ContainerExecInspect(ctx, s.ExecID)
	if err != nil || inspect.Running {
		return 0, false
	}
	return inspect.ExitCode, true
}

// ReadLspConfig reads and parses .ai/lsp-config.json from containerID's
// workspace. ファイルが存在しない/JSONとして壊れている場合はエラーにせず
// 空のマップを返す - 生徒が誤ってこのファイルを削除・破損させても、単に
// 「どの拡張子も未設定」という状態になるだけで、LSP機能全体や他の機能を
// 止めてはいけないため。
func ReadLspConfig(ctx context.Context, cli docker.ContainerAPI, containerID string) (map[string]string, error) {
	stdout, _, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{"cat", lspConfigPath})
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return map[string]string{}, nil
	}
	var config map[string]string
	if err := json.Unmarshal([]byte(stdout), &config); err != nil {
		return map[string]string{}, nil
	}
	return config, nil
}

// LspCommandForFile resolves the launch command for filePath's extension
// using config(ReadLspConfigの結果)。拡張子は先頭の"."を除いた形をキーと
// して扱う(設定ファイル側も"py"のようにドット無しで書く想定)。
func LspCommandForFile(config map[string]string, filePath string) (string, error) {
	ext := strings.TrimPrefix(path.Ext(filePath), ".")
	if ext == "" {
		return "", ErrLspLanguageNotConfigured
	}
	command, ok := config[ext]
	if !ok || strings.TrimSpace(command) == "" {
		return "", ErrLspLanguageNotConfigured
	}
	return command, nil
}

// StartLspSession execs command(.ai/lsp-config.jsonから引いた起動コマンド
// 文字列)をcontainerID内の`sh -c`経由で起動する - 生徒が
// `typescript-language-server --stdio`のような引数付きコマンドを、
// シェルのクォーティング等を気にせず素直に書けるようにするため
// (argv分割を自前で行わない)。標準入出力はTTYなしでアタッチする
// (LSPプロトコルはプレーンなJSON-RPCであり、疑似端末は不要かつ有害)。
//
// 起動前にサーバー全体の同時起動数の枠(tryAcquireLspSlot)を1つ確保する -
// 上限に達していればErrLspConcurrencyLimitReachedを返し、そもそもexecを
// 試みない。確保した枠は、返したLspSessionのCloseが呼ばれるまで保持される。
func StartLspSession(ctx context.Context, cli docker.ContainerAPI, containerID, command string) (*LspSession, error) {
	if !tryAcquireLspSlot() {
		return nil, ErrLspConcurrencyLimitReached
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", command},
		WorkingDir:   sandboxWorkspacePath,
		Tty:          false,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		releaseLspSlot()
		return nil, err
	}

	hijacked, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		releaseLspSlot()
		return nil, err
	}

	return &LspSession{ExecID: execResp.ID, Conn: hijacked.Conn, Reader: hijacked.Reader}, nil
}
