package handler

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
	photo, err := service.SaveAndAnalyze(h.DB, userId, req, file)

	c.JSON(http.StatusCreated, photo)
}

// Ai作成のカードを送信
func (h *Handler) AiCard(c *gin.Context) {
	userId, ok := utils.GetUserID(c) // APIのトークンを検証
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return
	}
	// JSONを受け取るための構造体
	var req struct {
		CourseID uint `json:"course_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDが必要です"})
		return
	}
	isJoined, err := db.IsStudentInCourse(h.DB, userId, req.CourseID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "DBエラー"})
		return
	}
	if !isJoined {
		c.JSON(http.StatusForbidden, gin.H{"error": "このクラスには参加していません"})
		return
	}
	aicard, err := db.AiSearchDB(h.DB, req.CourseID)
	c.JSON(http.StatusOK, gin.H{
		"aicard": aicard,
	})

}

func (h *Handler) AiCreation(c *gin.Context) {
	jst, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		// 万が一ロケーションの読み込みに失敗した場合は、固定で9時間進める（FixedZone）
		jst = time.FixedZone("Asia/Tokyo", 9*60*60)
	}
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "プロジェクトIDが必要です"})
		return
	}
	createdAt, err := service.AICreation(h.DB, userId, isTeacher, req.ProjectId)

	// 【すでに作成中のケース】時間が返ってきている場合
	if !createdAt.IsZero() {
		createdAtJST := createdAt.In(jst)
		c.JSON(http.StatusConflict, gin.H{
			"error": "すでにAIを作成中です。",
			"time":  createdAtJST.Format("2006-01-02 15:04"),
		})
		return
	}

	//【本当にエラーが起きたケース】err が nil でない場合
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	//【正常終了のケース】エラーもなく、重複もない場合
	c.JSON(http.StatusOK, gin.H{
		"message":    "AIの作成処理を開始しました。",
		"aicreation": createdAt,
	})

}
