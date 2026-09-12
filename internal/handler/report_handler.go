package handler

import (
	"net/http"
	"strings"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/utils"

	"github.com/gin-gonic/gin"
)

type reportContentRequest struct {
	// PublishSlug: 通報対象の公開URL(https://preview.a-kiis.com/{slug}/)の
	// パス先頭部分。対象が既に非公開/削除済みでも通報自体は受け付ける。
	PublishSlug string `json:"publish_slug" binding:"required"`
	Reason      string `json:"reason" binding:"required"`
}

const reportReasonMaxLength = 2000

// ReportPublishedContent handles POST /api/v2/report(NextPlan.md フェーズ7
// 「通報機能」)。ログイン済みユーザーのみ通報できる - 匿名通報は荒らし/
// スパムの温床になりやすく、この教育用サンドボックスの規模では「誰が
// 通報したか」が分かる方が運用上扱いやすいため。自動判定・自動非公開化は
// 一切行わない(記録するだけ) - 教員が一覧を見て手動対応する運用を想定
// (ContentReportモデルのコメント参照)。
func (h *Handler) ReportPublishedContent(c *gin.Context) {
	userID, authed := utils.GetUserID(c)
	if !authed {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return
	}

	var req reportContentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "通報理由を入力してください"})
		return
	}
	if len(reason) > reportReasonMaxLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "通報理由が長すぎます"})
		return
	}

	if err := db.CreateContentReport(h.DB, userID, req.PublishSlug, reason); err != nil {
		h.respondError(c, http.StatusInternalServerError, "通報受付", "通報の受付に失敗しました", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
