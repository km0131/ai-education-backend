package handler

import (
	"ai-education/backend/internal/db"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// 学習字のデータ取得
func (h *Handler) TrainingCurve(c *gin.Context) {
	type trainingCurve struct {
		ProjectUUID uuid.UUID `form:"project_id" json:"project_id"`
	}
	var req trainingCurve

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[Error] プロジェクトのパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}
	train, err := db.TrainingCurveDB(h.DB, req.ProjectUUID)
	if err != nil {
		log.Printf("[Error] 学習デターの取得に失敗しました。: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "学習デターの取得に失敗しました。"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"train": train.LearningCurve,
	})
}

// テストデータを取得
func (h *Handler) TestResults(c *gin.Context) {
	type trainingCurve struct {
		ProjectUUID uuid.UUID `form:"project_id" json:"project_id"`
	}
	var req trainingCurve

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[Error] プロジェクトのパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}
	test, err := db.TestResultsDB(h.DB, req.ProjectUUID)
	if err != nil {
		log.Printf("[Error] テストデターの取得に失敗しました。: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "テストデターの取得に失敗しました。"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"test": test,
	})
}

// テスト画像別正解を取得
func (h *Handler) TestResultsImge(c *gin.Context) {
	type trainingCurve struct {
		ProjectUUID uuid.UUID `form:"project_id" json:"project_id"`
	}
	var req trainingCurve

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[Error] プロジェクトのパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}
	testimge, err := db.TestResultsImageDB(h.DB, req.ProjectUUID)
	if err != nil {
		log.Printf("[Error] 学習デターの取得に失敗しました。: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "学習デターの取得に失敗しました。"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"testimge": testimge,
	})
}
