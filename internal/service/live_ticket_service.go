package service

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/google/uuid"
)

// LiveTicketTTL: ShellTicketTTL(ticket_service.go)と同じ理由・同じ長さの
// 短命ワンタイムチケット。講師による生徒セッションのリアルタイム監視・
// 共同操作(ライブセッション同期、NextPlan.md)のWebSocket
// (LiveShellWebSocket/LiveEditorWebSocket、live_session_handler.go)向け。
const LiveTicketTTL = 30 * time.Second

// LiveSessionRole distinguishes the student(セッションの主)、教師(割り込んで
// 参加する側)、そしてクラスメイト(Peer Viewer、読み取り専用で参加する側)を
// 区別する - サーバー側のハブ(live_session_hub.go)が「講師が参加/退出した」
// プレゼンス通知を出す判定、共有ターミナル(JoinShellHub)への参加可否、
// エディタ中継(JoinEditorHub)での送信内容の扱いに使う。
type LiveSessionRole string

const (
	LiveSessionRoleStudent LiveSessionRole = "student"
	LiveSessionRoleTeacher LiveSessionRole = "teacher"
	// LiveSessionRolePeer: 生徒間でのリアルタイム相互閲覧(Peer Viewer)機能の
	// 閲覧者側。エディタ中継には参加できる(対象生徒の変更/カーソルを受信する
	// ため)が、共有ターミナル(JoinShellHub)には参加できず、エディタ中継へ
	// 自分から送った内容は中継されない(read-only、live_session_hub.goの
	// JoinShellHub/JoinEditorHub参照)。
	LiveSessionRolePeer LiveSessionRole = "peer"
)

// LiveTicket carries everything the WebSocket handlers
// (LiveShellWebSocket/LiveEditorWebSocket)need once the ticket is consumed:
// どのセッション(TargetUserID+CourseID = 常に「生徒本人」を指す、発行者が
// 教師でも同じ)、どの役割(Role)、画面に出す表示名(DisplayName、講師が
// 参加した時のバナー「👨‍🏫 {DisplayName}がサポート中です」に使う)。
type LiveTicket struct {
	TargetUserID uuid.UUID
	CourseID     uint
	Role         LiveSessionRole
	DisplayName  string
}

type liveTicketEntry struct {
	ticket    LiveTicket
	expiresAt time.Time
}

// liveTicketStore: ticketStore(ticket_service.go)と同じ設計 - プロセス内
// メモリのみ、DB永続化なし(寿命30秒、単一インスタンス構成のため)。
var (
	liveTicketStoreMu sync.Mutex
	liveTicketStore   = map[string]liveTicketEntry{}
)

// IssueLiveTicket creates a new one-time ticket for joining(targetUserID,
// courseID)の共同セッションwith the given role/displayName。生徒本人が
// 自分のセッションへ参加する場合はrole=student・targetUserID=自分自身、
// 教師が生徒のセッションへ参加する場合はrole=teacher・targetUserID=対象の
// 生徒(呼び出し元のIssueOwnLiveSessionTicket/IssueTeacherLiveSessionTicket、
// live_session_handler.go参照 - どちらも権限確認は発行時点で完了済みとして
// この関数には渡す)。
func IssueLiveTicket(targetUserID uuid.UUID, courseID uint, role LiveSessionRole, displayName string) (ticketID string, expiresAt time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	ticketID = hex.EncodeToString(raw)
	expiresAt = time.Now().Add(LiveTicketTTL)

	liveTicketStoreMu.Lock()
	defer liveTicketStoreMu.Unlock()
	sweepExpiredLiveTicketsLocked()
	liveTicketStore[ticketID] = liveTicketEntry{
		ticket:    LiveTicket{TargetUserID: targetUserID, CourseID: courseID, Role: role, DisplayName: displayName},
		expiresAt: expiresAt,
	}
	return ticketID, expiresAt, nil
}

// ConsumeLiveTicket validates and immediately invalidates a ticket(one-time
// use、ConsumeShellTicketと同じ)。
func ConsumeLiveTicket(ticketID string) (LiveTicket, bool) {
	liveTicketStoreMu.Lock()
	defer liveTicketStoreMu.Unlock()

	entry, found := liveTicketStore[ticketID]
	delete(liveTicketStore, ticketID)
	if !found || time.Now().After(entry.expiresAt) {
		return LiveTicket{}, false
	}
	return entry.ticket, true
}

func sweepExpiredLiveTicketsLocked() {
	now := time.Now()
	for id, entry := range liveTicketStore {
		if now.After(entry.expiresAt) {
			delete(liveTicketStore, id)
		}
	}
}
