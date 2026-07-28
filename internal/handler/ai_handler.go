package handler

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	file, err := c.FormFile("file") // オリジナルファイル
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ファイル送信なし"})
		return
	}
	// フロントエンドで長辺512px以下にリサイズ済みの画像(任意。無ければサーバー側でリサイズする)
	resizedFile, _ := c.FormFile("resized_file")
	// Serviceを使って保存と分析開始
	photo, err := service.SaveAndAnalyze(h.DB, userId, req, file, resizedFile)
	if err != nil {
		log.Printf("[ERROR] UploadImage failed: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "画像の登録に失敗しました。", "filename": file.Filename})
		return
	}

	c.JSON(http.StatusCreated, photo)
}

// PhotoStatus: フロントでcreateImageBitmap/heic2anyがどちらも変換できなかった画像について、
// バックエンドのheif-convert/exiftoolによるバックグラウンド変換(非同期)の進行状況をまとめて返す。
// UploadImage/ImageUpdatedのレスポンスがconversion_status="processing"の場合、フロントはこの
// エンドポイントをポーリングして完了(ready)または失敗(failed)を確認する。
func (h *Handler) PhotoStatus(c *gin.Context) {
	userId, ok := utils.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return
	}
	var req model.ConversionStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "photo_idsは必須です"})
		return
	}

	statuses, err := db.GetPhotographConversionStatuses(h.DB, userId, req.PhotoIDs)
	if err != nil {
		log.Printf("[ERROR] PhotoStatus failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "変換状況の取得に失敗しました"})
		return
	}

	c.JSON(http.StatusOK, model.ConversionStatusResponse{Statuses: statuses})
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
	createdAt, excludedFailedCount, err := service.AICreation(h.DB, userId, isTeacher, req.ProjectId)

	// 【すでに作成中のケース】時間が返ってきている場合
	if !createdAt.IsZero() {
		createdAtJST := createdAt.In(jst)
		c.JSON(http.StatusConflict, gin.H{
			"error": "すでにAIを作成中です。",
			"time":  createdAtJST.Format("2006-01-02 15:04"),
		})
		return
	}

	// 【先生がこのクラスのAI作成/学習をブロックしているケース】
	if errors.Is(err, service.ErrAiCreationBlocked) {
		c.JSON(http.StatusLocked, gin.H{"error": err.Error()})
		return
	}

	//【本当にエラーが起きたケース】err が nil でない場合
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	//【正常終了のケース】エラーもなく、重複もない場合
	c.JSON(http.StatusOK, gin.H{
		"message":               "AIの作成処理を開始しました。",
		"aicreation":            createdAt,
		"excluded_failed_count": excludedFailedCount,
	})

}

func (h *Handler) AiModel(c *gin.Context) {
	type trainingCurve struct {
		ProjectUUID uuid.UUID `form:"project_id" json:"project_id"`
	}
	var req trainingCurve

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[Error] プロジェクトのパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}
	aimodel, err := db.AiModelDB(h.DB, req.ProjectUUID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"aimodel": aimodel,
	})
}

// 新しいハンドラー：AI学習済みモデル（tf.js形式）の提供ハンドラー
func (h *Handler) GetModelFile(c *gin.Context) {
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
	// /app/storage/models フォルダの中身を探しに行くように設定
	basePath := "/app/storage/models"
	fullPath := filepath.Join(basePath, cleanedPath)

	log.Printf("[DEBUG] 検索中のAIモデル絶対パス: %s", fullPath)
	// ファイルの実在チェック
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		h.respondError(c, 404, "ファイル取得", "AIモデルファイルが見つかりません", err)
		return
	}
	// ファイルが見つかったらGinのStaticFileで配信する
	c.Header("Cache-Control", "private, max-age=31536000, immutable")
	c.File(fullPath)
}
