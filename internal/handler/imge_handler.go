package handler

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"
)

// 画像一覧用
func (h *Handler) ImageAcquisition(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}

	var req struct {
		ProjectId uuid.UUID `json:"project_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	imge, err := db.ImageAcquisitionDB(h.DB, userId, isTeacher, req.ProjectId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "データの所得に失敗しました"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data": imge,
	})
}

// 画像更新用
func (h *Handler) ImageUpdated(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}

	var req model.ImageUploadRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDとカテゴリIDは必須です"})
		return
	}
	file, err := c.FormFile("file") // オリジナルファイル
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ファイル送信なし"})
		return
	}
	// フロントエンドで長辺512px以下にリサイズ済みの画像(任意。無ければサーバー側でリサイズする)
	resizedFile, _ := c.FormFile("resized_file")

	projectUUID, err := uuid.Parse(req.UploadSessionID)
	if err != nil {
		h.respondError(c, 400, "画像追加", "不正なUUIDフォーマットです", err)
		return
	}

	if isTeacher == false {
		author, err := db.AuthorCheck(h.DB, userId, projectUUID)
		if err != nil {
			log.Printf("[ERROR] 画像の更新失敗しました。: %v", err)
			c.JSON(http.StatusOK, gin.H{
				"err": "画像の更新失敗しました。",
			})
		}
		if !author {
			log.Printf("[ERROR] 画像を更新する権限が有りません。: %v", err)
			c.JSON(http.StatusOK, gin.H{
				"err": "画像を更新する権限が有りません。",
			})
		}
	}
	// Serviceを使って保存と分析開始
	photo, err := service.SaveAndAnalyze(h.DB, userId, req, file, resizedFile)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "画像の登録に失敗しました。"})
		return
	}

	err = db.UpTrainingJobSnapshot(h.DB, photo, projectUUID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "スナップショットの追加が失敗しました。"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"status": "完了",
	})
}

// 画像削除用
func (h *Handler) DeleteImage(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}

	type req struct {
		PhotoId     uint   `json:"photo_id" binding:"required"`
		ProjectUUID string `json:"project_id" binding:"required"`
	}
	var input req
	if err := c.ShouldBindBodyWith(&input, binding.JSON); err != nil {
		log.Printf("[ERROR] バインド失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "画像IDとプロジェクトIDは必要です。"})
		return
	}
	projectUUID, err := uuid.Parse(input.ProjectUUID)
	if err != nil {
		h.respondError(c, 400, "画像追加", "不正なUUIDフォーマットです", err)
		return
	}

	if isTeacher == false {
		author, err := db.AuthorCheck(h.DB, userId, projectUUID)
		if err != nil {
			log.Printf("[ERROR] 画像の更新失敗しました。: %v", err)
			c.JSON(http.StatusOK, gin.H{
				"err": "画像の更新失敗しました。",
			})
		}
		if !author {
			log.Printf("[ERROR] 画像を更新する権限が有りません。: %v", err)
			c.JSON(http.StatusOK, gin.H{
				"err": "画像を更新する権限が有りません。",
			})
		}
	}

	err = db.DeletedImageDB(h.DB, input.PhotoId)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "画像の削除に失敗しました。"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"status": "完了",
	})

}
