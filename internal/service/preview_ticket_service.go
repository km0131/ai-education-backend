package service

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/google/uuid"
)

// PreviewTicketTTL: シェル/LSPの短命ワンタイムチケット(ShellTicketTTL、
// ticket_service.go)と違い、Webプレビューのiframeは1ページの表示だけで
// HTML本体・CSS・JS・画像など何本ものHTTPリクエストを行う。1回使ったら
// 失効するチケットでは2本目以降のリクエストが即座に401になってしまうため、
// この期間内は使い回せる(=検証時に消費しない)チケットにしている - 期限が
// 来れば生徒側がプレビューを開き直すことで新しいチケットを取得する想定
// (IDE内蔵Webプレビュー、VS CodeのSimple Browser相当)。
const PreviewTicketTTL = 10 * time.Minute

type previewTicket struct {
	userID    uuid.UUID
	courseID  uint
	expiresAt time.Time
}

// previewTicketStoreは既存のticketStore(ticket_service.go)とは別に持つ -
// あちらは「検証すると即座に消費される」設計そのものが違うため、共有すると
// 挙動が混ざってしまう。
var (
	previewTicketMu    sync.Mutex
	previewTicketStore = map[string]previewTicket{}
)

// IssuePreviewTicket creates a new reusable ticket for this (user, course)
// pair, valid for PreviewTicketTTL.
func IssuePreviewTicket(userID uuid.UUID, courseID uint) (ticketID string, expiresAt time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	ticketID = hex.EncodeToString(raw)
	expiresAt = time.Now().Add(PreviewTicketTTL)

	previewTicketMu.Lock()
	defer previewTicketMu.Unlock()
	sweepExpiredPreviewTicketsLocked()
	previewTicketStore[ticketID] = previewTicket{userID: userID, courseID: courseID, expiresAt: expiresAt}
	return ticketID, expiresAt, nil
}

// ValidatePreviewTicket checks a ticket WITHOUT consuming it(shell/LSPの
// ConsumeShellTicketと違い、1つのプレビューセッション中に何度も呼ばれる
// ため)。期限切れ/存在しないticketIDはok=falseを返す。
func ValidatePreviewTicket(ticketID string) (userID uuid.UUID, courseID uint, ok bool) {
	previewTicketMu.Lock()
	defer previewTicketMu.Unlock()

	t, found := previewTicketStore[ticketID]
	if !found || time.Now().After(t.expiresAt) {
		delete(previewTicketStore, ticketID)
		return uuid.Nil, 0, false
	}
	return t.userID, t.courseID, true
}

func sweepExpiredPreviewTicketsLocked() {
	now := time.Now()
	for id, t := range previewTicketStore {
		if now.After(t.expiresAt) {
			delete(previewTicketStore, id)
		}
	}
}
