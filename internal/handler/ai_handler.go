package handler

import (
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"
	"net/http"

	"github.com/gin-gonic/gin"
)

// 画像保存
func (h *Handler) UploadImage(c *gin.Context) {
	userId, ok := utils.GetUserID(c) // APIのトークンを検証
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return
	}
	var req model.ImageUploadRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDとカテゴリIDは必須です"})
		return
	}
	file, err := c.FormFile("file") // ファイル
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ファイル送信なし"})
		return
	}
	// Serviceを使って保存と分析開始
	photo, err := service.SaveAndAnalyze(h.DB, userId, req.CourseID, req.CategoryID, req.Title, file)

	c.JSON(http.StatusCreated, photo)
}
