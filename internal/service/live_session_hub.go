package service

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"sync"

	"ai-education/backend/internal/docker"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// 講師による生徒セッションのリアルタイム監視・共同操作(ライブセッション
// 同期、Pair Programming / Live Share形式)。1人の生徒のセッションを、
// 生徒本人のWebSocket接続と、割り込んで参加した講師のWebSocket接続とで
// 共有する - シェルは1つの共有ptyプロセスへの入出力を全接続へブロード
// キャストし(JoinShellHub)、エディタは生徒/講師のMonacoが送ってくる
// 変更差分/カーソル位置イベントを他の全接続へそのまま中継するだけ
// (JoinEditorHub、サーバー側は中身を解釈しない - OT/CRDTのような競合
// 解決は行わない、2人ペア前提の単純な中継)。
//
// この2つのチャンネルは(生徒のUserID, コースID)ごとに1つのliveSessionに
// 束ねられ、必要になった時(最初の接続)に遅延生成し、誰も繋がっていない
// 状態になったら破棄する(liveSessions、パッケージレベルのレジストリ)。

// liveClient wraps one WebSocket connection attached to a liveSession -
// gorilla/websocketは1コネクションにつき同時に1つの書き込みしか許さない
// ため、書き込みは必ずこのmuを介す(共有ptyの出力ブロードキャスト・
// エディタイベントの中継のどちらも複数goroutineから同時に書き込み得る)。
type liveClient struct {
	conn *websocket.Conn
	role LiveSessionRole
	name string
	mu   sync.Mutex
}

func (c *liveClient) writeBinary(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, data)
}

func (c *liveClient) writeText(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// liveSession bundles both channels for one(生徒のUserID, コースID)ペア。
type liveSession struct {
	mu sync.Mutex

	shellClients map[*liveClient]struct{}
	shellExec    *ShellSession

	editorClients map[*liveClient]struct{}
}

var (
	liveSessionsMu sync.Mutex
	liveSessions   = map[string]*liveSession{}
)

func liveSessionKey(targetUserID uuid.UUID, courseID uint) string {
	return targetUserID.String() + ":" + strconv.FormatUint(uint64(courseID), 10)
}

func getOrCreateLiveSession(key string) *liveSession {
	liveSessionsMu.Lock()
	defer liveSessionsMu.Unlock()
	s, ok := liveSessions[key]
	if !ok {
		s = &liveSession{
			shellClients:  map[*liveClient]struct{}{},
			editorClients: map[*liveClient]struct{}{},
		}
		liveSessions[key] = s
	}
	return s
}

// removeLiveSessionIfEmpty tears down the registry entry once nobody is
// attached to either channel.
func removeLiveSessionIfEmpty(key string, s *liveSession) {
	s.mu.Lock()
	empty := len(s.shellClients) == 0 && len(s.editorClients) == 0
	s.mu.Unlock()
	if !empty {
		return
	}
	liveSessionsMu.Lock()
	defer liveSessionsMu.Unlock()
	// 削除する前にもう一度確認する(ロックを持ち直す間に別のJoinが入った
	// 場合の競合を避けるため)。
	s.mu.Lock()
	stillEmpty := len(s.shellClients) == 0 && len(s.editorClients) == 0
	s.mu.Unlock()
	if stillEmpty && liveSessions[key] == s {
		delete(liveSessions, key)
	}
}

// livePresenceMessage is broadcast over the editor channel to the student
// whenever a teacher joins/leaves either channel - 生徒画面上部の
// 「👨‍🏫 {name}がサポート中です」バナー表示のトリガーに使う
// (WorkspaceLayout.tsx)。サーバー側で解釈するJSONメッセージはこれだけで、
// それ以外(editor-change/editor-cursor)は中身を見ずにそのまま中継する。
type livePresenceMessage struct {
	Type          string `json:"type"` // 常に"presence"
	TeacherJoined bool   `json:"teacher_joined"`
	Name          string `json:"name,omitempty"`
}

func (s *liveSession) broadcastPresence(joined bool, name string) {
	data, err := json.Marshal(livePresenceMessage{Type: "presence", TeacherJoined: joined, Name: name})
	if err != nil {
		return
	}
	s.mu.Lock()
	targets := make([]*liveClient, 0, len(s.editorClients))
	for c := range s.editorClients {
		if c.role == LiveSessionRoleStudent {
			targets = append(targets, c)
		}
	}
	s.mu.Unlock()
	for _, c := range targets {
		_ = c.writeText(data)
	}
}

// liveShellControlMessage mirrors shellClientMessage(shell_handler.go)だが、
// serviceパッケージ側で完結させるために別途定義している(handlerパッケージの
// 非公開型を跨いで再利用できないため)。
type liveShellControlMessage struct {
	Type string `json:"type"`
	Cols uint   `json:"cols"`
	Rows uint   `json:"rows"`
}

// JoinShellHub attaches conn to the shared pty for(targetUserID, courseID)
// - 最初の接続(生徒本人が最初のことが多いが、講師が先に繋がっても構わない)
// で共有ptyを起動し(StartShellSession)、以降の接続は同じptyにぶら下がる。
// 呼び出し元(LiveShellWebSocket、live_session_handler.go)がブロックする間、
// このconnからの入力をptyへ転送し続ける - ptyの出力ブロードキャストは
// 最初の接続時に一度だけ起動するpumpSharedShellOutputが一括で担当する。
func JoinShellHub(ctx context.Context, cli docker.ContainerAPI, containerID string, targetUserID uuid.UUID, courseID uint, conn *websocket.Conn, role LiveSessionRole, name string) error {
	key := liveSessionKey(targetUserID, courseID)
	session := getOrCreateLiveSession(key)

	session.mu.Lock()
	if session.shellExec == nil {
		exec, err := StartShellSession(ctx, cli, containerID, 80, 24)
		if err != nil {
			session.mu.Unlock()
			return err
		}
		session.shellExec = exec
		go pumpSharedShellOutput(session)
	}
	client := &liveClient{conn: conn, role: role, name: name}
	session.shellClients[client] = struct{}{}
	session.mu.Unlock()

	if role == LiveSessionRoleTeacher {
		session.broadcastPresence(true, name)
	}

readLoop:
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			break readLoop
		}
		session.mu.Lock()
		exec := session.shellExec
		session.mu.Unlock()
		if exec == nil {
			break readLoop
		}
		switch messageType {
		case websocket.BinaryMessage:
			if _, werr := exec.Conn.Write(data); werr != nil {
				break readLoop
			}
		case websocket.TextMessage:
			var msg liveShellControlMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
				if err := exec.Resize(ctx, cli, msg.Cols, msg.Rows); err != nil {
					log.Printf("[LIVE-SHELL] リサイズに失敗しました exec=%s err=%v", exec.ExecID, err)
				}
			}
		}
	}

	session.mu.Lock()
	delete(session.shellClients, client)
	remaining := len(session.shellClients)
	session.mu.Unlock()

	if role == LiveSessionRoleTeacher {
		session.broadcastPresence(false, name)
	}
	if remaining == 0 {
		session.mu.Lock()
		if session.shellExec != nil {
			session.shellExec.Close()
			session.shellExec = nil
		}
		session.mu.Unlock()
		removeLiveSessionIfEmpty(key, session)
	}
	return nil
}

// pumpSharedShellOutput reads the shared pty's output once and broadcasts
// each chunk to every attached shell client - 1つのptyを複数接続で
// 共有するための唯一の読み取りゴルーチン(execの標準出力を読むのはこの
// goroutineだけ)。ptyが終了したら、繋がっている全接続を能動的にCloseし、
// それぞれのJoinShellHub呼び出し側のreadLoopを解放する。
func pumpSharedShellOutput(session *liveSession) {
	session.mu.Lock()
	exec := session.shellExec
	session.mu.Unlock()
	if exec == nil {
		return
	}

	buf := make([]byte, 4096)
	for {
		n, err := exec.Reader.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			session.mu.Lock()
			targets := make([]*liveClient, 0, len(session.shellClients))
			for c := range session.shellClients {
				targets = append(targets, c)
			}
			session.mu.Unlock()
			for _, c := range targets {
				_ = c.writeBinary(chunk)
			}
		}
		if err != nil {
			break
		}
	}

	session.mu.Lock()
	exec.Close()
	session.shellExec = nil
	clients := make([]*liveClient, 0, len(session.shellClients))
	for c := range session.shellClients {
		clients = append(clients, c)
	}
	session.mu.Unlock()
	for _, c := range clients {
		_ = c.conn.Close()
	}
}

// JoinEditorHub attaches conn to the pure relay channel for(targetUserID,
// courseID) - 受け取ったテキストフレームを、送信者以外の全接続へそのまま
// 中継するだけ(Monacoの変更差分/カーソル位置イベント、フロント側の
// useLiveEditorSync.ts参照)。ブロックする間ずっとこのconnからの
// メッセージを読み続け、切断されたら後片付けする。
func JoinEditorHub(targetUserID uuid.UUID, courseID uint, conn *websocket.Conn, role LiveSessionRole, name string) {
	key := liveSessionKey(targetUserID, courseID)
	session := getOrCreateLiveSession(key)

	client := &liveClient{conn: conn, role: role, name: name}
	session.mu.Lock()
	session.editorClients[client] = struct{}{}
	session.mu.Unlock()

	if role == LiveSessionRoleTeacher {
		session.broadcastPresence(true, name)
	}

readLoop:
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			break readLoop
		}
		if messageType != websocket.TextMessage {
			continue
		}
		if client.role == LiveSessionRolePeer {
			// 生徒間でのリアルタイム相互閲覧(Peer Viewer)は読み取り専用 -
			// フロント側はMonacoをreadOnly化しており本来何も送られてこない
			// はずだが、サーバー側でも防御的に中継しない(受信専用として
			// 扱う)。接続自体は維持し、他の参加者からの変更/カーソルは
			// 引き続き受信できる。
			continue
		}

		session.mu.Lock()
		targets := make([]*liveClient, 0, len(session.editorClients))
		for c := range session.editorClients {
			if c != client {
				targets = append(targets, c)
			}
		}
		session.mu.Unlock()
		for _, c := range targets {
			_ = c.writeText(data)
		}
	}

	session.mu.Lock()
	delete(session.editorClients, client)
	remaining := len(session.editorClients)
	session.mu.Unlock()

	if role == LiveSessionRoleTeacher {
		session.broadcastPresence(false, name)
	}
	if remaining == 0 {
		removeLiveSessionIfEmpty(key, session)
	}
}
