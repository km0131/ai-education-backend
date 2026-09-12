package service

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"sync"
	"time"

	"ai-education/backend/internal/db"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ShellTicketTTL is how long an issued ticket remains valid before it
// expires (NextPlan.md §3.2: 有効期限30秒程度)。
const ShellTicketTTL = 30 * time.Second

type shellTicket struct {
	userID    uuid.UUID
	courseID  uint
	expiresAt time.Time
}

// ticketStore is an in-memory, single-process store for short-lived one-time
// WebSocket tickets (シェル/LSP接続用、NextPlan.md §3.2)。チケットはPASETO認証済みの
// HTTPリクエストから IssueShellTicket() で発行され、その後WebSocket接続時に
// (長期間有効なPASETOトークンの代わりに)クエリパラメータとして渡す想定
// (トークン自体がURL/アクセスログに残るリスクを避けるため)。
//
// あえてDBに永続化していない: チケットの寿命は約30秒しかなく、サーバー再起動で
// 消えても実害がない。また現状は研究運用段階の単一インスタンス構成のため
// (NextPlan.md §3.5: 物理分離は対象人数拡大時に検討)、プロセス内メモリで十分。
var (
	ticketStoreMu sync.Mutex
	ticketStore   = map[string]shellTicket{}
)

// IssueShellTicket creates a new one-time ticket for this (user, course)
// pair and returns its ID and expiry. Opportunistically sweeps expired
// tickets so the map doesn't grow unbounded.
//
// It also touches the sandbox's LastActiveAt (best-effort - a student
// requesting a shell/LSP ticket is the clearest "about to use this sandbox"
// signal the API currently exposes, since the WebSocket endpoints that
// actually consume the ticket aren't implemented yet, フェーズ3・6参照).
// A touch failure only means the idle-timeout sweeper (NextPlan.md フェーズ2)
// might stop the sandbox a little early - it must never block ticket
// issuance itself, so the error is logged, not returned.
func IssueShellTicket(tx *gorm.DB, userID uuid.UUID, courseID uint) (ticketID string, expiresAt time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	ticketID = hex.EncodeToString(raw)
	expiresAt = time.Now().Add(ShellTicketTTL)

	ticketStoreMu.Lock()
	defer ticketStoreMu.Unlock()
	sweepExpiredTicketsLocked()
	ticketStore[ticketID] = shellTicket{userID: userID, courseID: courseID, expiresAt: expiresAt}

	if err := db.TouchProgramSandboxActivity(tx, userID, courseID); err != nil {
		log.Printf("[SHELL-TICKET] サンドボックスの活動時刻更新に失敗しました(チケット発行自体は継続します) user=%s course=%d err=%v", userID, courseID, err)
	}

	return ticketID, expiresAt, nil
}

// ConsumeShellTicket validates and immediately invalidates a ticket
// (one-time use: it is removed from the store whether or not it was still
// valid). Returns ok=false if the ticket doesn't exist, was already used, or
// has expired. Intended to be called by the future WebSocket upgrade handler
// (シェル/LSP中継、NextPlan.md フェーズ3・6、本チケット発行エンドポイントの時点では未実装)
// before accepting the connection.
func ConsumeShellTicket(ticketID string) (userID uuid.UUID, courseID uint, ok bool) {
	ticketStoreMu.Lock()
	defer ticketStoreMu.Unlock()

	t, found := ticketStore[ticketID]
	delete(ticketStore, ticketID)
	if !found || time.Now().After(t.expiresAt) {
		return uuid.Nil, 0, false
	}
	return t.userID, t.courseID, true
}

func sweepExpiredTicketsLocked() {
	now := time.Now()
	for id, t := range ticketStore {
		if now.After(t.expiresAt) {
			delete(ticketStore, id)
		}
	}
}
