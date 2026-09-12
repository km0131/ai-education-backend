package service

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"ai-education/backend/internal/db"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 単一サブドメイン(preview.a-kiis.com)パスベースルーティングによる外部公開
// 機能(NextPlan.md フェーズ7)。新しいドメイン/ワイルドカードDNSを取得せず、
// 1つの固定サブドメイン配下のパス(/{PublishSlug}/...)だけで生徒ごとの
// Webアプリを配信する - IDE内蔵Webプレビュー(preview_handler.go、
// preview_service.go)と同じticket/exec-tunnelの考え方を、認証済みセッション
// 限定ではなく「有効期限内なら誰でも閲覧できる」形に広げたもの。

const (
	defaultPublishDurationHours = 24
	defaultPublishMaxConcurrent = 10
)

// PublishDefaultDuration reads PUBLISH_DEFAULT_DURATION_HOURS from the
// environment, falling back to defaultPublishDurationHours when unset or
// invalid - lspMaxConcurrent/LspIdleTimeout(lsp_service.go)と同じ「.envで
// 調整可能」という方針を踏襲する。
func PublishDefaultDuration() time.Duration {
	hours := defaultPublishDurationHours
	if v := os.Getenv("PUBLISH_DEFAULT_DURATION_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			hours = n
		}
	}
	return time.Duration(hours) * time.Hour
}

// PublishMaxConcurrent reads PUBLISH_MAX_CONCURRENT from the environment,
// falling back to defaultPublishMaxConcurrent when unset or invalid。
func PublishMaxConcurrent() int {
	if v := os.Getenv("PUBLISH_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultPublishMaxConcurrent
}

// PublishBaseURL reads PUBLISH_BASE_URL(末尾スラッシュなし、例:
// "https://preview.a-kiis.com")from the environment, falling back to the
// production既定値。生成する公開URLの組み立てにだけ使う - ルーティング
// 自体はHostヘッダーの一致で行う(cmd/main.goのhostRouter参照)ため、この値
// 自体はレスポンス文言にしか影響しない。
func PublishBaseURL() string {
	if v := os.Getenv("PUBLISH_BASE_URL"); v != "" {
		return v
	}
	return "https://preview.a-kiis.com"
}

var (
	// ErrPublishConcurrencyLimitReached is returned by PublishSandbox when
	// システム全体の同時公開数(PublishMaxConcurrent)が既に上限に達している場合。
	ErrPublishConcurrencyLimitReached = errors.New("同時公開数の上限に達しています。しばらくしてから再度お試しください")
	// ErrPublishInvalidPort is returned when the caller-specified port is
	// out of the valid TCP port range.
	ErrPublishInvalidPort = errors.New("ポート番号が不正です")
	// ErrPublishNotPublished is returned by ExtendPublish/UnpublishSandbox
	// when the caller's sandbox isn't currently published.
	ErrPublishNotPublished = errors.New("公開されていません")
)

const publishSlugAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
const publishSlugLength = 12

// generatePublishSlug builds a random, unguessable path segment for the
// public preview URL - 生徒のUserID(UUID、本人のアカウントに紐づく)を
// そのまま公開URLに晒さないための使い捨てトークン。DB上の既存slugと衝突
// した場合は最大数回まで再生成する(publishSlugLength=12の英数字なら
// 36^12通りあり、実運用規模では衝突はまず起きない - あくまで保険)。
func generatePublishSlug(tx *gorm.DB) (string, error) {
	const maxAttempts = 5
	buf := make([]byte, publishSlugLength)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for i, b := range buf {
			buf[i] = publishSlugAlphabet[int(b)%len(publishSlugAlphabet)]
		}
		slug := string(buf)
		exists, err := db.ExistsPublishSlug(tx, slug)
		if err != nil {
			return "", err
		}
		if !exists {
			return slug, nil
		}
	}
	return "", fmt.Errorf("公開用の識別子を発行できませんでした")
}

// PublishSandbox marks (userID, courseID)'s sandbox as published, generating
// a fresh PublishSlug and setting PublishExpiresAt to now + PublishDefaultDuration()。
// 呼び出し元(publish_handler.go)は事前にsandboxContainerIDOrRespond相当で
// コンテナが存在し起動中であることを確認しておくこと(公開の意味が無い停止
// 中のコンテナを公開状態にしてしまわないため)。
func PublishSandbox(tx *gorm.DB, userID uuid.UUID, courseID uint, port int) (slug string, expiresAt time.Time, err error) {
	if port <= 0 || port > 65535 {
		return "", time.Time{}, ErrPublishInvalidPort
	}

	count, err := db.CountPublishedSandboxes(tx)
	if err != nil {
		return "", time.Time{}, err
	}
	if count >= int64(PublishMaxConcurrent()) {
		return "", time.Time{}, ErrPublishConcurrencyLimitReached
	}

	slug, err = generatePublishSlug(tx)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = time.Now().Add(PublishDefaultDuration())

	if err := db.PublishProgramSandbox(tx, userID, courseID, slug, port, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return slug, expiresAt, nil
}

// UnpublishSandbox stops publishing(userID, courseID)'s sandbox - 生徒本人の
// 明示操作(「公開を停止する」ボタン)から呼ばれる。
func UnpublishSandbox(tx *gorm.DB, userID uuid.UUID, courseID uint) error {
	return db.UnpublishProgramSandbox(tx, userID, courseID)
}

// ExtendPublish pushes(userID, courseID)'s publish expiry forward by another
// PublishDefaultDuration() - 既に切れかけ/切れている場合に備え、「今」と
// 「現在の期限」のうち遅い方を起点にする(既存の残り時間を無駄にしない)。
// 公開中でない場合はErrPublishNotPublishedを返す。
func ExtendPublish(tx *gorm.DB, userID uuid.UUID, courseID uint) (newExpiresAt time.Time, err error) {
	sandbox, err := db.FindProgramSandbox(tx, userID, courseID)
	if err != nil {
		return time.Time{}, err
	}
	if sandbox == nil || !sandbox.Published {
		return time.Time{}, ErrPublishNotPublished
	}

	base := time.Now()
	if sandbox.PublishExpiresAt.After(base) {
		base = sandbox.PublishExpiresAt
	}
	newExpiresAt = base.Add(PublishDefaultDuration())

	if err := db.ExtendProgramSandboxPublish(tx, userID, courseID, newExpiresAt); err != nil {
		return time.Time{}, err
	}
	return newExpiresAt, nil
}
