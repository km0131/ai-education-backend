package handler

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/utils"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// 説明文の作成で必要なラベル情報を取得
func (h *Handler) GetDescription(c *gin.Context) {
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
	label, err := db.LabelGet(h.DB, userId, isTeacher, req.ProjectId)

	// エラー処理
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err})
		return
	}

	// 返却
	c.JSON(http.StatusOK, gin.H{
		"label": label,
	})
}

func (h *Handler) CreateDescription(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	var req struct {
		ProjectId    uuid.UUID      `json:"project_id" binding:"required"`
		Explanations map[int]string `json:"explanations" binding:"required"` // まとめて送信
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "プロジェクトIDが必要です"})
		return
	}
	err := db.LabelCreation(h.DB, userId, isTeacher, req.ProjectId, req.Explanations)
	// エラー処理
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err})
		return
	}

	// 返却
	c.JSON(http.StatusOK, gin.H{
		"message": "すべてのせつめい文をほぞんしたよ！",
	})
}

// 新しいハンドラー：AI学習用画像の提供ハンドラー
func (h *Handler) GetAiPhotographImage(c *gin.Context) {
	// 既存の PostPasswordImage と同じロジックを流用

	filename := c.Param("filename")

	// Ginの仕様上、先頭に「/」が入るため、それを取り除く
	filename = strings.TrimPrefix(filename, "/")

	// セキュリティ対策: filepath.Clean で「../」などを排除
	cleanedPath := filepath.Clean(filename)

	// 「../」を使って親ディレクトリに遡ろうとする攻撃だけをブロックする
	if strings.HasPrefix(cleanedPath, "..") {
		h.respondError(c, 403, "ファイル取得", "不正なファイルアクセスを検知しました", nil)
		return
	}

	// 今回用のコンテナ内の絶対パスをベースにする
	// /app/images/ai_photogrph フォルダの中身を探しに行くように設定
	basePath := "/app/images/ai_photogrph"
	fullPath := filepath.Join(basePath, cleanedPath)

	log.Printf("[DEBUG] 検索中のAI写真絶対パス: %s", fullPath)
	// ファイルの実在チェック
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		h.respondError(c, 404, "ファイル取得", "AI写真が見つかりませんでした", err)
		return
	}
	// ファイルが見つかったらGinのStaticFileで配信する
	c.File(fullPath)
}

// 新しいハンドラー：AI学習用画像の提供ハンドラー
func (h *Handler) GetTestImage(c *gin.Context) {
	// 既存の PostPasswordImage と同じロジックを流用

	filename := c.Param("filename")

	// Ginの仕様上、先頭に「/」が入るため、それを取り除く
	filename = strings.TrimPrefix(filename, "/")

	// セキュリティ対策: filepath.Clean で「../」などを排除
	cleanedPath := filepath.Clean(filename)

	// 「../」を使って親ディレクトリに遡ろうとする攻撃だけをブロックする
	if strings.HasPrefix(cleanedPath, "..") {
		h.respondError(c, 403, "ファイル取得", "不正なファイルアクセスを検知しました", nil)
		return
	}

	// 今回用のコンテナ内の絶対パスをベースにする
	// /app/images/ai_photogrph フォルダの中身を探しに行くように設定
	basePath := "/app/images/test_photogrph"
	fullPath := filepath.Join(basePath, cleanedPath)

	log.Printf("[DEBUG] テスト画像が見つかりません: %s", fullPath)
	// ファイルの実在チェック
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		h.respondError(c, 404, "ファイル取得", "テスト画像が見つかりません", err)
		return
	}
	// ファイルが見つかったらGinのStaticFileで配信する
	c.File(fullPath)
}

func (h *Handler) UpLabel(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	type UpLabel struct {
		ConfigID     uuid.UUID `form:"config_id" json:"config_id"`
		OldLabelName string    `form:"old_label_name" json:"old_label_name"`
		NewLabelName string    `form:"new_label_name" json:"new_label_name"`
	}
	var req UpLabel

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[Error] ラベル変更のパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}

	uplabel, err := db.UpLabelDB(h.DB, userId, isTeacher, req.ConfigID, req.OldLabelName, req.NewLabelName)
	if err != nil {
		log.Printf("[Error] ラベル変更に失敗しました。: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "ラベル変更に失敗しました"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"uplabel": uplabel,
	})
}
