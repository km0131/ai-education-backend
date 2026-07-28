package handler

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/utils"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) MyCourses(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}

	if isTeacher {
		// 先生が「作成した」クラスを取得
		courses, err := db.TeacherClassSearch(h.DB, userId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "作成したクラス一覧の取得に失敗しました"})
			return
		}

		// 先生が「参加した」クラスを取得
		studentcourses, err := db.ClassTeacherSearch(h.DB, userId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "参加したクラス一覧の取得に失敗しました"})
			return
		}

		// フロント（Next.js）へ両方まとめて返す
		c.JSON(http.StatusOK, gin.H{
			"status":         "success",
			"teacher":        isTeacher,
			"courses":        courses,        // 作成したクラス
			"studentcourses": studentcourses, // 参加したクラス
		})
		return
	} else {
		// 生徒の場合は「参加したクラス」のみ取得
		studentcourses, err := db.ClassTeacherSearch(h.DB, userId)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "クラス一覧の取得に失敗しました"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status":         "success",
			"teacher":        isTeacher,
			"studentcourses": studentcourses,
		})
	}
}

func (h *Handler) CreateClass(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}

	// 先生かどうかのチェック（生徒なら即座に弾く）
	if !isTeacher {
		c.JSON(http.StatusForbidden, gin.H{"error": "クラスを作成する権限がありません"})
		return
	}

	// リクエストボディ（JSON）の読み込みとパース
	var input model.CreateClassInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不正なリクエストデータです"})
		return
	}

	// 5. 先ほど作成したDB関数を呼び出す
	course, err := db.CreateCourse(h.DB, input.ClassName, input.Description, userId)
	if err != nil {
		// 招待コード生成失敗かDB保存失敗か、必要に応じてエラー内容でハンドリングしてもOK
		c.JSON(http.StatusInternalServerError, gin.H{"error": "クラスの作成に失敗しました"})
		return
	}

	// 成功レスポンス
	c.JSON(http.StatusCreated, gin.H{
		"message":    "クラスを作成しました",
		"class_code": course.InviteCode,
	})
}

// クラス参加
func (h *Handler) JoinClass(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	var input model.CreateClassOutput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不正なリクエストデータです"})
		return
	}

	course, err := db.ClassSearch(h.DB, input.InviteCode)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "クラスが存在しません"})
		return
	}
	if isTeacher {
		if course.TeacherID == userId {
			c.JSON(http.StatusBadRequest, gin.H{"error": "自分が作成したクラスに参加することはできません"})
			return
		}
	}
	err = db.RegisterStudentToCourse(h.DB, userId, course.ID)
	if err != nil {
		// すでに参加済みエラーの場合
		if errors.Is(err, db.ErrAlreadyJoined) {
			c.JSON(http.StatusConflict, gin.H{"error": "あなたは既にこのクラスに参加しています"})
			return
		}

		// その他のDBエラーの場合
		c.JSON(http.StatusInternalServerError, gin.H{"error": "クラスへの参加処理に失敗しました"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "クラスに参加しました！"})
}

// AiCreationBlockStatus: 指定クラスでAIの新規作成/学習開始/性能テストがブロックされているかを返す。
// 先生・生徒どちらのロールからも呼べる(教師停止中であることを生徒側にも伝えるため)。
func (h *Handler) AiCreationBlockStatus(c *gin.Context) {
	_, ok := utils.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}

	var req struct {
		CourseID uint `json:"course_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDは必須です"})
		return
	}

	blocked, err := db.IsAiCreationBlocked(h.DB, req.CourseID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "クラスが見つかりません"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ai_creation_blocked": blocked})
}

// SetAiCreationBlockStatus: AIの新規作成/学習開始/性能テストのブロック状態を切り替える。
// 先生のみ、かつ自分が作成したクラスに対してのみ実行できる(生徒は403で弾く)。
func (h *Handler) SetAiCreationBlockStatus(c *gin.Context) {
	isTeacher, ok := utils.GetUserTeacher(c)
	userId, ok1 := utils.GetUserID(c)
	if !ok || !ok1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	if !isTeacher {
		c.JSON(http.StatusForbidden, gin.H{"error": "先生以外はこの操作を行えません"})
		return
	}

	var req struct {
		CourseID uint `json:"course_id" binding:"required"`
		Blocked  bool `json:"blocked"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDは必須です"})
		return
	}

	if err := db.SetAiCreationBlocked(h.DB, req.CourseID, userId, req.Blocked); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "このクラスを操作する権限がないか、クラスが存在しません"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ai_creation_blocked": req.Blocked})
}

// クラス名取得
func (h *Handler) RemoveClass(c *gin.Context) {
	classID := c.Param("id")
	if classID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "クラスIDが指定されていません"})
		return
	}
	userId, ok := utils.GetUserID(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "認証情報の取得または型変換に失敗しました"})
		return
	}
	res, err := db.GetClassDetailsForUser(h.DB, classID, userId)
	if err != nil {
		// エラー内容に応じてメッセージを返却
		c.JSON(http.StatusForbidden, gin.H{"error": "このクラスへのアクセス権限がないか、存在しません"})
		return
	}

	// 4. そのままフロントへJSONを返却
	c.JSON(http.StatusOK, res)

}
