package handler

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// テスト画像の登録
func (h *Handler) UploadingTestImage(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	if isTeacher == false {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "先生以外は登録出来ません"})
		return
	}
	var req model.ImageUploadResponse
	if err := c.ShouldBind(&req); err != nil {
		log.Printf("[Error] バインド失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDとラベルは必要です"})
		return
	}
	file, err := c.FormFile("file") // オリジナルファイル
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ファイル送信なし"})
		return
	}
	// フロントエンドで長辺512px以下にリサイズ済みの画像(任意。無ければサーバー側でリサイズする)
	resizedFile, _ := c.FormFile("resized_file")
	err = service.CreatingTestDataset(h.DB, req, file, resizedFile)
	if err != nil {
		log.Printf("[Error] テスト画像の登録に失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "テストデータの保存に失敗しました"})
		return
	}
}

// テスト画像を取得
func (h *Handler) GetImage(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	if isTeacher == false {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "先生以外は取得出来ません"})
		return
	}

	type GetImage struct {
		CourseID uint `form:"course_id" json:"course_id"`
	}
	var req GetImage
	if err := c.ShouldBind(&req); err != nil {
		log.Printf("[Error] バインド失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDは必要です"})
		return
	}
	img, err := db.GetImageDB(h.DB, req.CourseID)
	if err != nil {
		log.Printf("[Error] テスト画像の取得に失敗しました。: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "テスト画像の取得に失敗しました"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"img": img,
	})
}

func (h *Handler) DeleteTestImage(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	if isTeacher == false {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "先生以外は取得出来ません"})
		return
	}
	type GetImage struct {
		ID int `form:"photo_id" json:"photo_id"`
	}
	var req GetImage

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[Error] 削除リクエストのパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}

	err := db.DeleteImageDB(h.DB, req.ID)
	if err != nil {
		log.Printf("[Error] 画像の削除に失敗しました。: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "画像の削除に失敗しました。"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status": "削除に成功しました。",
	})
}

func (h *Handler) GetTestLabel(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	if isTeacher == false {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "先生以外は取得出来ません"})
		return
	}

	type GetLabel struct {
		CourseID uint `form:"course_id" json:"course_id"`
	}
	var req GetLabel

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[Error] ラベルIDのパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}

	labels, err := db.GetTestLabelDB(h.DB, req.CourseID)

	if err != nil {
		log.Printf("[Error] リストの取得に失敗しました: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "リストの取得に失敗しました"})
		return
	}

	c.JSON(http.StatusOK, model.TestLabelsResponse{
		Labels: labels,
	})
}

func (h *Handler) UpTestLabel(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	if isTeacher == false {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "先生以外は変更"})
		return
	}
	type UpLabel struct {
		CourseID     uint   `form:"course_id" json:"course_id"`
		OldLabelName string `form:"old_label_name" json:"old_label_name"`
		NewLabelName string `form:"new_label_name" json:"new_label_name"`
	}

	var req UpLabel

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[Error] ラベル変更のパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}

	uplabel, err := db.UpTestLabelDB(h.DB, req.CourseID, req.OldLabelName, req.NewLabelName)
	if err != nil {
		log.Printf("[Error] ラベル変更に失敗しました。: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "ラベル変更に失敗しました"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"uplabel": uplabel,
	})
}

// テストラベルを取得
func (h *Handler) GetTestLabelMap(c *gin.Context) {
	type UpLabel struct {
		ProjectUUID uuid.UUID `form:"project_uuid" json:"project_uuid"`
	}
	var req UpLabel
	if err := c.ShouldBind(&req); err != nil {
		log.Printf("[Error] プロジェクトIDのパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}
	intermediatelabel, err := db.GetStudentTestMapping(h.DB, req.ProjectUUID)
	if err != nil {
		log.Printf("[Error]　テストラベルの中間テーブルの取得に失敗しました。: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "テストラベルの中間テーブルの取得に失敗しました"})
		return
	}
	labels := make([]string, 0, len(intermediatelabel))
	mappings := make([]model.MappingResponse, 0, len(intermediatelabel))
	for _, m := range intermediatelabel {
		labels = append(labels, m.TeacherLabelName)

		mappings = append(mappings, model.MappingResponse{
			StudentLabelName: m.StudentLabelName,
			TeacherLabelName: m.TeacherLabelName,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"labels": mappings,
	})
}

func (h *Handler) UpStudentTestLabel(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	var req model.SaveMappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[Error] パース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}
	err := db.UpStudentTestLabelDB(h.DB, req, userId, isTeacher)
	if err != nil {
		log.Printf("[Error] DBの更新に失敗しました: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "DBの更新に失敗しました"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success"})
}

func (h *Handler) TestExecution(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	type UpLabel struct {
		ProjectUUID uuid.UUID `form:"project_uuid" json:"project_uuid"`
		CourseID    uint      `form:"course_id" json:"course_id"`
	}
	var req UpLabel
	if err := c.ShouldBind(&req); err != nil {
		log.Printf("[Error] プロジェクトIDのパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}
	testTime, err := service.TestExecutionService(h.DB, req.ProjectUUID, req.CourseID, isTeacher, userId)
	if err != nil {
		if !testTime.IsZero() {
			log.Printf("[INFO] 現在テスト実行中です。開始時間: %v", testTime)
			c.JSON(http.StatusBadRequest, gin.H{
				"time": testTime,
			})
			return
		} else {
			log.Printf("[Error] ステータスの作成に失敗しました: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "ステータスの作成に失敗しました"})
			return
		}
	}
}

func (h *Handler) ImageEvaluationGet(c *gin.Context) {
	type UpLabel struct {
		ProjectUUID uuid.UUID `form:"project_uuid" json:"project_uuid"`
	}
	var req UpLabel
	if err := c.ShouldBind(&req); err != nil {
		log.Printf("[Error] プロジェクトIDのパース失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストです"})
		return
	}
	summary, err := db.GetImageEvaluationDB(h.DB, req.ProjectUUID)
	if err != nil {
		log.Printf("[Error] 画像評価情報の取得失敗: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "画像評価情報の取得に失敗しました"})
		return
	}

	c.JSON(http.StatusOK, summary)
}
