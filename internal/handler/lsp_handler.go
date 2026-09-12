package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"ai-education/backend/internal/docker"
	"ai-education/backend/internal/service"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// lspIdleCheckInterval is how often relayLsp's idle-watchdog goroutine checks
// for inactivity. idleSweepInterval(idle_sandbox_service.go)と同じ考え方 -
// 実運用でチューニングする対象はLSP_IDLE_TIMEOUT_MINUTES自体であって、この
// 監視の間隔は内部実装の詳細でしかない。セッションは設定値より最大でこの
// 間隔ぶんだけ長く生き残ることがあるが、許容できるマージン。
const lspIdleCheckInterval = 30 * time.Second

// WebSocketクローズコード(RFC 6455 7.4.2、4000-4999はプライベート利用域)。
// 「編集中のみ起動+アイドル自動停止」による意図した切断(lspCloseCodeIdleTimeout)
// と、言語サーバープロセス自体が予期せず終了した切断(lspCloseCodeProcessCrashed)
// をフロント(lspProtocol.ts/useLspDocument.ts)が区別できるようにするための
// 合図。前者は「次に実際の編集があるまで黙って待つ」既存の遅延再接続のまま、
// 後者はフロント側がバックオフ付きで自動リトライし、それでも復旧しない場合に
// ユーザーへ通知する(LSPプロセスクラッシュ時のリトライ・ユーザー通知フロー、
// NextPlan.md フェーズ6)。上記以外の理由(ブラウザ側がWebSocketを閉じた等)は
// 明示コードを送らず、ブラウザ標準の異常切断コード(1006等)に任せる -
// フロント側はこれも「idle以外はすべて再試行対象」として扱うため、判定漏れは
// 起きない。
const (
	lspCloseCodeIdleTimeout    = 4000
	lspCloseCodeProcessCrashed = 4001
)

// LspWebSocket upgrades to a WebSocket and relays a language server process
// inside the caller's sandbox container(汎用LSP中継、NextPlan.md フェーズ6)。
// 「pyright専用」ではなく、リクエストされたファイルの拡張子を
// .ai/lsp-config.json(ReadLspConfig/LspCommandForFile、lsp_service.go)で
// 引いた起動コマンドをそのままexecする汎用ロジック - 生徒が別言語のLSP
// サーバーをインストールしてこのファイルに追記するだけで、新規UIパネル無しに
// 対応言語を増やせる。
//
// シェル機能(shell_handler.go)と同じ理由でAuthMiddlewareの外に置き、
// IssueShellTicket()が発行した短命ワンタイムチケットで認証する(ticketは
// (userID, courseID)にしか紐づかない汎用の値で、シェル用/LSP用を区別しない
// - どちらのWebSocketも結局「このユーザーのこのコースのサンドボックスへの
// 接続」を許可するだけなので、専用のチケット種別を新設する必要がない)。
//
// ワイヤプロトコル: LSPの生のJSON-RPC(Content-Lengthフレーミング)を
// そのままバイナリフレームでやり取りする(制御メッセージなし - シェルの
// resizeに相当する概念がLSPには無い)。
func (h *Handler) LspWebSocket(c *gin.Context) {
	ticket := c.Query("ticket")
	if ticket == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "チケットが指定されていません"})
		return
	}
	userID, courseID, ok := service.ConsumeShellTicket(ticket)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "チケットが無効か期限切れです"})
		return
	}

	filePath := c.Query("path")
	if filePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pathは必須です"})
		return
	}

	ctx := c.Request.Context()
	containerID, err := service.ContainerIDForSession(ctx, h.DockerClient, userID, courseID)
	if err != nil {
		status, msg := http.StatusInternalServerError, "サンドボックスへの接続に失敗しました"
		switch {
		case errors.Is(err, service.ErrSandboxNotFound):
			status, msg = http.StatusNotFound, "サンドボックスが見つかりません"
		case errors.Is(err, service.ErrSandboxNotRunning):
			status, msg = http.StatusConflict, "サンドボックスが起動していません。再開してから接続してください"
		}
		c.JSON(status, gin.H{"error": msg})
		return
	}

	config, err := service.ReadLspConfig(ctx, h.DockerClient, containerID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "LSP設定取得", "言語サーバー設定の取得に失敗しました", err)
		return
	}
	command, err := service.LspCommandForFile(config, filePath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// execセッションはWebSocketへアップグレードする前に開始する
	// (ShellWebSocketと同じ理由 - アップグレード後は通常のJSONエラー
	// レスポンスをもう返せないため)。
	session, err := service.StartLspSession(ctx, h.DockerClient, containerID, command)
	if err != nil {
		if errors.Is(err, service.ErrLspConcurrencyLimitReached) {
			// 同時起動数の上限(LSP_MAX_CONCURRENT、NextPlan.md フェーズ6)。
			// 待機列には入れず、素直に混雑エラーとして返す(ErrSandboxPoolExhausted
			// と同じ方針)。
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		log.Printf("[LSP-WS] 言語サーバーの起動に失敗しました user=%s course=%d container=%s command=%q err=%v", userID, courseID, containerID, command, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "言語サーバーの起動に失敗しました"})
		return
	}
	defer session.Close()

	conn, err := shellUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[LSP-WS] WebSocketへのアップグレードに失敗しました user=%s course=%d err=%v", userID, courseID, err)
		return
	}
	defer conn.Close()

	relayLsp(ctx, h.DockerClient, conn, session)
}

// lspWsWriter adapts a gorilla websocket.Conn to io.Writer so stdcopy.StdCopy
// (下記relayLsp参照)が復元した標準出力の生バイト列を、そのままWebSocketの
// バイナリフレームとして転送できるようにする。書き込みのたびにlastActivity
// (relayLspのアイドル監視用)を更新する。
type lspWsWriter struct {
	conn         *websocket.Conn
	lastActivity *atomic.Int64
}

func (w lspWsWriter) Write(p []byte) (int, error) {
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	w.lastActivity.Store(time.Now().UnixNano())
	return len(p), nil
}

// relayLsp pumps bytes bidirectionally between the WebSocket connection and
// the attached LSP session, until either side finishes - the browser closing
// the WebSocket, the language server process exiting(正常終了・クラッシュの
// 両方を含む), or (「編集中のみ起動」、NextPlan.md フェーズ6)
// LSP_IDLE_TIMEOUT_MINUTESの間どちらの方向にも入出力が無かった場合。
//
// 終了理由をフロントへ伝えるため、WebSocketを生で閉じる前に必ずclose
// フレーム(sendClose)を送る。アイドルタイムアウトは明示的に
// lspCloseCodeIdleTimeoutを送り、フロントはこれを「意図した切断」として
// 静かに扱う(次に実際の編集操作があった時だけ再接続、lspManager.ts/
// useLspDocument.ts参照)。それ以外の理由で言語サーバー側(標準出力)が
// 先に終わった場合は、execの終了コードを調べて0以外なら
// lspCloseCodeProcessCrashedを送る - フロント側はこれをバックオフ付きの
// 自動リトライ+ユーザー通知のトリガーにする(LSPプロセスクラッシュ時の
// リトライ・ユーザー通知フロー)。ブラウザ側が先に切断した場合は、もう
// 相手に何かを送る意味がないため何も送らない。
//
// シェル中継(shell_handler.goのrelayShell)と違い、LspSessionはTty:falseで
// 起動している - Dockerの非TTY exec attachは標準出力/標準エラーを1本の
// 接続に多重化して送ってくる(8バイトヘッダー+ペイロードの繰り返し)ため、
// stdcopy.StdCopyで復元してからでないと、生のバイト列をそのままLSPの
// JSON-RPCとしてブラウザ側へ渡すことができない(ヘッダーのゴミが混ざって
// プロトコルが壊れる)。標準エラー(言語サーバー自身のログ等でJSON-RPCの
// 一部ではない)はio.Discardへ捨てる。
func relayLsp(ctx context.Context, cli docker.ContainerAPI, conn *websocket.Conn, session *service.LspSession) {
	// done: どちらのポンプ(stdout側/クライアント側)が先に終わったかを
	// 値そのもので伝える(チャネルcloseの順序に依存する曖昧さを避けるため、
	// bool値の送受信で表現する)。
	const pumpStdout = true
	const pumpClient = false
	done := make(chan bool, 2)
	stopWatchdog := make(chan struct{})
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())

	// sendCloseは最初の1回だけWebSocketのクローズフレームを実際に送る
	// (sync.Onceで多重送信を防ぐ - アイドル監視と読み取りループが競合して
	// 両方から呼ばれても安全)。
	var closeOnce sync.Once
	sendClose := func(code int, reason string) {
		closeOnce.Do(func() {
			deadline := time.Now().Add(2 * time.Second)
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), deadline)
		})
	}

	go func() {
		_, _ = stdcopy.StdCopy(lspWsWriter{conn: conn, lastActivity: &lastActivity}, io.Discard, session.Reader)
		done <- pumpStdout
	}()

	go func() {
	readLoop:
		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				break readLoop
			}
			if messageType == websocket.BinaryMessage {
				lastActivity.Store(time.Now().UnixNano())
				if _, err := session.Conn.Write(data); err != nil {
					break readLoop
				}
			}
		}
		done <- pumpClient
	}()

	go func() {
		idleTimeout := service.LspIdleTimeout()
		ticker := time.NewTicker(lspIdleCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatchdog:
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, lastActivity.Load())) >= idleTimeout {
					sendClose(lspCloseCodeIdleTimeout, "アイドルタイムアウトのため言語サーバーを停止しました")
					session.Close()
					_ = conn.Close()
					return
				}
			}
		}
	}()

	// どちらか一方が終わった時点で(シェル終了・切断・アイドルタイムアウトの
	// どれが先でも)両方の接続を閉じてもう片方のブロックを解除し、両方の
	// 終了を待ってから返る。ウォッチドッグは既に閉じられた接続を重ねて
	// closeするだけなので、ここでの二重closeは無害(net.Conn.Close()と同じ)。
	first := <-done
	close(stopWatchdog)

	// 標準出力側(=言語サーバープロセスの出力)が先に終わった場合だけ、
	// プロセスの終了コードを調べる。ブラウザ側が先に切断した場合や、
	// アイドル監視が既にsendCloseを送っている場合は、ここでの判定は
	// 意味を持たない(sendCloseはsync.Onceで冪等なので、二重に呼んでも
	// 実害はない)。
	if first == pumpStdout {
		if code, ok := session.ExitCode(ctx, cli); ok && code != 0 {
			sendClose(lspCloseCodeProcessCrashed, fmt.Sprintf("言語サーバーが予期せず終了しました(exit=%d)", code))
		}
	}

	session.Close()
	_ = conn.Close()
	<-done
}
