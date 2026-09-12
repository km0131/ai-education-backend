package handler

import (
	"errors"
	"net/http"
	"time"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 単一サブドメイン(preview.a-kiis.com)パスベース公開機能(NextPlan.md
// フェーズ7)の、生徒本人向け管理API(公開開始/停止/延長/状態取得)。
// 実際に外部からプレビューを閲覧する経路はこれとは別のPublicPreviewProxy
// (public_preview_handler.go、未認証・Hostヘッダーpreview.a-kiis.com専用)
// が担う - こちらは常にPASETO認証済みセッション(AuthMiddleware)の下でのみ
// 呼ばれる、通常のprogram/containerグループの一員。

type publishRequest struct {
	CourseID uint `json:"course_id" binding:"required"`
	Port     int  `json:"port" binding:"required"`
}

// publishURL builds the public-facing preview URL for slug(PublishBaseURL
// + "/" + slug + "/") - レスポンスに含めるだけの表示用文字列で、実際の
// ルーティングはHostヘッダー一致(cmd/main.goのhostRouter)で行う。
func publishURL(slug string) string {
	return service.PublishBaseURL() + "/" + slug + "/"
}

// PublishSandboxContainer starts publishing the caller's sandbox at a fresh
// PublishSlug(POST /program/container/publish)。呼び出し元のコンテナが
// running状態であることを事前に確認する - 起動していないコンテナを公開
// 状態にしても閲覧できるものが無く意味が無いため。
func (h *Handler) PublishSandboxContainer(c *gin.Context) {
	var req publishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}
	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}
	if _, ok := h.sandboxContainerIDOrRespond(c, userID, req.CourseID); !ok {
		return
	}

	var slug string
	var expiresAt time.Time
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		slug, expiresAt, txErr = service.PublishSandbox(tx, userID, req.CourseID, req.Port)
		return txErr
	})
	switch {
	case errors.Is(err, service.ErrPublishInvalidPort):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case errors.Is(err, service.ErrPublishConcurrencyLimitReached):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "公開開始", "公開の開始に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"slug":       slug,
		"url":        publishURL(slug),
		"expires_at": expiresAt,
	})
}

type unpublishRequest struct {
	CourseID uint `json:"course_id" binding:"required"`
}

// UnpublishSandboxContainer stops publishing the caller's sandbox
// (POST /program/container/unpublish、「公開を停止する」ボタン)。
func (h *Handler) UnpublishSandboxContainer(c *gin.Context) {
	var req unpublishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}
	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}

	if err := service.UnpublishSandbox(h.DB, userID, req.CourseID); err != nil {
		h.respondError(c, http.StatusInternalServerError, "公開停止", "公開の停止に失敗しました", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"published": false})
}

type extendPublishRequest struct {
	CourseID uint `json:"course_id" binding:"required"`
}

// ExtendSandboxPublish pushes the caller's publish expiry forward by another
// PublishDefaultDuration()(POST /program/container/publish/extend、
// 「公開期間を延長する」ボタン)。
func (h *Handler) ExtendSandboxPublish(c *gin.Context) {
	var req extendPublishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}
	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}

	var expiresAt time.Time
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		expiresAt, txErr = service.ExtendPublish(tx, userID, req.CourseID)
		return txErr
	})
	switch {
	case errors.Is(err, service.ErrPublishNotPublished):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "公開延長", "公開期間の延長に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expires_at": expiresAt})
}

// GetSandboxPublishStatus returns the caller's own current publish state
// (GET /program/container/publish-status?course_id=)。公開中でなければ
// published=falseのみを返す(フロントのカウントダウンUIが表示可否を
// 判断できるようにするため)。
func (h *Handler) GetSandboxPublishStatus(c *gin.Context) {
	userID, courseID, ok := h.authorizeSandboxFileQuery(c)
	if !ok {
		return
	}

	sandbox, err := db.FindProgramSandbox(h.DB, userID, courseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "公開状態取得", "公開状態の取得に失敗しました", err)
		return
	}
	if sandbox == nil || !sandbox.Published {
		c.JSON(http.StatusOK, gin.H{"published": false})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"published":  true,
		"slug":       sandbox.PublishSlug,
		"url":        publishURL(sandbox.PublishSlug),
		"port":       sandbox.PublishPort,
		"expires_at": sandbox.PublishExpiresAt,
	})
}
