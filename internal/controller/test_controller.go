package controller

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// HandleTestReady はPythonからのテスト実行結果コールバックを受け取る
// (/tmp/.../run_test_and_notify_go が投げる success/error 通知の受け口)
func HandleTestReady(c *gin.Context) {
	database := db.DB

	var input model.TestResultCallbackInput
	if err := c.ShouldBindJSON(&input); err != nil {
		log.Printf("[ERROR] テスト結果コールバックのバリデーション失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストパラメータです"})
		return
	}

	if err := db.SaveTestResultDB(database, input); err != nil {
		log.Printf("[ERROR] テスト結果の保存に失敗しました (StatusID: %d): %v", input.StatusID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "テスト結果の保存に失敗しました"})
		return
	}

	log.Printf("[INFO] StatusID: %d のテスト結果を保存しました (status: %s)", input.StatusID, input.Status)
	c.JSON(http.StatusOK, gin.H{"status": "success", "status_id": input.StatusID})
}
