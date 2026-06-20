package handler

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) MyCourses(c *gin.Context) {
	userId, isTeacher, ok := getAuthUser(c)
	if !ok {
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
	userId, isTeacher, ok := getAuthUser(c)
	if !ok {
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
	userId, isTeacher, ok := getAuthUser(c)
	if !ok {
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

// ユーザー情報を一括で安全に取得する共通ヘルパー
func getAuthUser(c *gin.Context) (uuid.UUID, bool, bool) {
	userIdAny, existsId := c.Get("UserID")
	isTeacherAny, existsTeacher := c.Get("UserTeacher")

	if !existsId || !existsTeacher {
		return uuid.Nil, false, false
	}

	userId, ok1 := userIdAny.(uuid.UUID)
	isTeacher, ok2 := isTeacherAny.(bool)
	if !ok1 || !ok2 {
		return uuid.Nil, false, false
	}

	return userId, isTeacher, true
}

// クラス名取得
func (h *Handler) RemoveClass(c *gin.Context) {
	classID := c.Param("id")
	if classID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "クラスIDが指定されていません"})
		return
	}
	userIdAny, existsId := c.Get("UserID")
	if !existsId {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証情報が見つかりません"})
		return
	}
	userId, ok1 := userIdAny.(uuid.UUID)
	if !ok1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "型変換エラー"})
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
