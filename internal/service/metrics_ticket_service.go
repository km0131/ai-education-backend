package service

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MetricsTicketTTL is how long an issued ticket remains valid before it
// expires(ticket_service.go/live_ticket_service.goと同じ有効期限30秒の
// 使い切りチケット方式)。
const MetricsTicketTTL = 30 * time.Second

type metricsTicket struct {
	teacherID uuid.UUID
	expiresAt time.Time
}

// metricsTicketStore is an in-memory, single-process store for short-lived
// one-time WebSocket tickets(リソース使用状況ダッシュボードのMetricsWebSocket
// 用) - ブラウザのWebSocket APIはAuthorizationヘッダーを付けられないため、
// シェル/LSP/ライブセッションと同じ理由でPASETOの代わりにこのチケットで
// 認証する。
var (
	metricsTicketMu    sync.Mutex
	metricsTicketStore = map[string]metricsTicket{}
)

// IssueMetricsTicket creates a new one-time ticket authenticating the caller
// (呼び出し前に「先生であること」の確認はhandler側で済ませている)。
func IssueMetricsTicket(teacherID uuid.UUID) (ticketID string, expiresAt time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	ticketID = hex.EncodeToString(raw)
	expiresAt = time.Now().Add(MetricsTicketTTL)

	metricsTicketMu.Lock()
	defer metricsTicketMu.Unlock()
	sweepExpiredMetricsTicketsLocked()
	metricsTicketStore[ticketID] = metricsTicket{teacherID: teacherID, expiresAt: expiresAt}

	return ticketID, expiresAt, nil
}

// ConsumeMetricsTicket validates and immediately invalidates a ticket
// (one-time use)。
func ConsumeMetricsTicket(ticketID string) (teacherID uuid.UUID, ok bool) {
	metricsTicketMu.Lock()
	defer metricsTicketMu.Unlock()

	t, found := metricsTicketStore[ticketID]
	delete(metricsTicketStore, ticketID)
	if !found || time.Now().After(t.expiresAt) {
		return uuid.Nil, false
	}
	return t.teacherID, true
}

func sweepExpiredMetricsTicketsLocked() {
	now := time.Now()
	for id, t := range metricsTicketStore {
		if now.After(t.expiresAt) {
			delete(metricsTicketStore, id)
		}
	}
}
