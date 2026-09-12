package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"ai-education/backend/internal/docker"
	"ai-education/backend/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// shellUpgrader upgrades the HTTP connection to a WebSocket for the shell
// relay. CheckOrigin is permissive, matching this app's existing CORS policy
// (cmd/main.go: AllowOrigins "*") - access control for this endpoint is the
// short-lived one-time ticket consumed before upgrading (see
// ShellWebSocket), not the browser Origin header.
var shellUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// shellClientMessage is the only client->server WebSocket *text* frame this
// relay understands. Every *binary* frame from the client is instead
// forwarded byte-for-byte to the shell's stdin - see relayShell.
type shellClientMessage struct {
	Type string `json:"type"`
	Cols uint   `json:"cols"`
	Rows uint   `json:"rows"`
}

// ShellWebSocket upgrades to a WebSocket and relays an interactive shell
// inside the caller's sandbox container (NextPlan.md フェーズ3「コンテナ内
// プロセスへのexecをGoから起動し、標準入出力をWebSocketにストリーミング」)。
//
// Unlike the rest of the /program routes, this endpoint does NOT sit behind
// AuthMiddleware: a browser's native WebSocket API can't attach a custom
// Authorization header, so per NextPlan.md §3.2 it instead authenticates
// with the short-lived one-time ticket issued by IssueShellTicket, passed as
// ?ticket=. The ticket is validated and consumed (single use) before the
// connection is upgraded - a stolen/replayed WS URL is worthless after the
// legitimate client's first connection attempt, and the ticket itself
// expires in ShellTicketTTL regardless.
//
// Wire protocol once connected (see shellClientMessage):
//   - server -> client: binary frames = the shell's raw stdout+stderr
//     (merged, since the exec runs with a TTY)
//   - client -> server: binary frames = raw stdin bytes; text frames = JSON
//     control messages, currently only {"type":"resize","cols":N,"rows":N}
func (h *Handler) ShellWebSocket(c *gin.Context) {
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

	// execセッションはWebSocketへアップグレードする前に開始する。こうすると
	// 起動失敗時も通常のJSONエラーレスポンス(他のエンドポイントと同じ形)を
	// 返せる - アップグレード後はHTTPレスポンスをもう書けず、WSフレーム越しに
	// 独自のエラー表現を用意しないといけなくなるため。
	//
	// 初期ターミナルサイズは80x24固定。実際のブラウザ側サイズへは、接続直後に
	// フロントから送られてくるresize制御メッセージで追従させる想定
	// (xterm.js側の実装はNextPlan.md フェーズ3の別項目「フロントのxterm.js⇔
	// WebSocketのストリーミング経路を実装」で行う)。
	session, err := service.StartShellSession(ctx, h.DockerClient, containerID, 80, 24)
	if err != nil {
		log.Printf("[SHELL-WS] execセッションの開始に失敗しました user=%s course=%d container=%s err=%v", userID, courseID, containerID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "シェルの起動に失敗しました"})
		return
	}
	defer session.Close()

	conn, err := shellUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[SHELL-WS] WebSocketへのアップグレードに失敗しました user=%s course=%d err=%v", userID, courseID, err)
		return
	}
	defer conn.Close()

	relayShell(ctx, conn, session, h.DockerClient)
}

// relayShell pumps bytes bidirectionally between the WebSocket connection
// and the attached exec session until either side finishes - the browser
// closing the WebSocket, or the shell process exiting (e.g. the student
// types "exit") - at which point it tears down both so the other pump's
// blocking Read unblocks with an error too, instead of leaking a goroutine
// forever waiting on the side that already ended.
//
// Only one goroutine ever calls conn.WriteMessage (the outbound pump below)
// and only the other goroutine (the inbound pump) writes to session.Conn -
// net.Conn and gorilla's websocket.Conn both require a single writer at a
// time, but tolerate one concurrent reader + one concurrent writer, which is
// exactly this shape.
func relayShell(ctx context.Context, conn *websocket.Conn, session *service.ShellSession, cli docker.ContainerAPI) {
	done := make(chan struct{}, 2)

	// コンテナ→ブラウザ: execの標準出力(Tty=trueによりstdout/stderr合成済み)を
	// そのままバイナリフレームで転送する。シェルプロセスが終了する(Readerが
	// エラー/EOFを返す)と、このgoroutineが先に終わる。
	//
	// 転送は常に生のバイトをそのまま先に送る - guideScannerはその後に「補足の
	// ガイドメッセージを追加で送るかどうか」だけを判定する(タスク指示書§2
	// タスク2、shell_output_guide.go)。判定・追記が入出力そのものを遅延・改変
	// することはない。
	go func() {
		defer func() { done <- struct{}{} }()
		guideScanner := newOutputGuideScanner()
		buf := make([]byte, 4096)
		for {
			n, err := session.Reader.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, chunk); writeErr != nil {
					return
				}
				for _, guide := range guideScanner.Scan(chunk) {
					if writeErr := conn.WriteMessage(websocket.BinaryMessage, guide); writeErr != nil {
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// ブラウザ→コンテナ: バイナリフレーム=標準入力、テキストフレーム=
	// resize等の制御メッセージ(現状はresizeのみ)。ブラウザがWebSocketを
	// 閉じると、このgoroutineが先に終わる。
	go func() {
		defer func() { done <- struct{}{} }()
	readLoop:
		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				break readLoop
			}
			switch messageType {
			case websocket.BinaryMessage:
				if _, err := session.Conn.Write(data); err != nil {
					break readLoop
				}
			case websocket.TextMessage:
				var msg shellClientMessage
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				if msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
					if err := session.Resize(ctx, cli, msg.Cols, msg.Rows); err != nil {
						log.Printf("[SHELL-WS] リサイズに失敗しました exec=%s err=%v", session.ExecID, err)
					}
				}
			}
		}
	}()

	// どちらか一方が終わった時点で(シェル終了・切断のどちらが先でも)両方の
	// 接続を閉じてもう片方のブロックを解除し、両方の終了を待ってから返る。
	<-done
	session.Close()
	_ = conn.Close()
	<-done
}
