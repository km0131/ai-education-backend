package service

import (
	"log"
	"strings"
)

// suspiciousPhrases is a minimal, best-effort keyword list used to flag
// obviously phishing-shaped pages(単一サブドメイン公開機能の悪用対策、
// NextPlan.md フェーズ7「簡易コンテンツスキャン」)。単純な文字列一致では
// 正当なページを誤検知しうるため、ここでは自動でブロック/非公開化はせず、
// サーバーログに警告を残すだけにとどめる(ミドルウェアフックとして用意する
// のみ、というタスク要件どおり) - 実際に対応するかどうかは、通報機能
// (ContentReport、report_handler.go)経由で人間(教員)が判断する運用を想定。
var suspiciousPhrases = []string{
	"クレジットカード番号",
	"パスワードを入力してください",
	"暗証番号",
	"credit card number",
	"enter your password",
	"verify your account",
}

// ScanResponseBodyForAbuse inspects an already-decoded HTML response body
// for suspiciousPhrases and logs a warning tagged with the publish slug (for
// later cross-reference against ContentReport)。本文の書き換えは一切
// 行わない(検知のみ、配信は妨げない) - NewPublicPreviewReverseProxy
// (preview_service.go)のModifyResponseから呼ばれる。
func ScanResponseBodyForAbuse(body []byte, slug string) {
	lower := strings.ToLower(string(body))
	for _, phrase := range suspiciousPhrases {
		if strings.Contains(lower, strings.ToLower(phrase)) {
			log.Printf("[PUBLISH-CONTENT-SCAN] ⚠ 公開ページに注意フレーズを検出しました slug=%s phrase=%q", slug, phrase)
			return
		}
	}
}
