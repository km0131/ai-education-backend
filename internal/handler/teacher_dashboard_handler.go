package handler

import (
	"errors"
	"net/http"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 教師ダッシュボード(TeacherDashboardModal.tsx)向けの操作API。生徒本人向け
// の同名操作(publish_handler.go/program_handler.go)とは別に切り出している -
// 対象がAuthMiddlewareの本人(userID)ではなくクラス内の任意の生徒になる
// ため、権限確認の形が異なる(utils.GetUserTeacherで「先生であること」を
// 安価に確認した上で、実際の所有権はdb.IsCourseTeacher/各serviceが
// courses.teacher_idとの一致で確認する二段構え)。

// authorizeTeacherRequest binds course_id, verifies the caller is
// authenticated AND is *a* teacher(PASETOクレーム上)。実際にこのコースの
// 担当教師かどうかまでは確認しない(そこはservice層がDBで確認する、
// SetAiCreationBlockedと同じ二段構え) - ここでは「そもそも先生ですらない
// 一般生徒からの呼び出し」を安価に弾くだけ。
func (h *Handler) authorizeTeacherRequest(c *gin.Context, req any) (teacherID uuid.UUID, ok bool) {
	uid, authed := utils.GetUserID(c)
	if !authed {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return uuid.Nil, false
	}
	isTeacher, teacherOk := utils.GetUserTeacher(c)
	if !teacherOk || !isTeacher {
		c.JSON(http.StatusForbidden, gin.H{"error": "先生以外はこの操作を行えません"})
		return uuid.Nil, false
	}
	if err := c.ShouldBindJSON(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return uuid.Nil, false
	}
	return uid, true
}

// respondTeacherServiceError maps the teacher-dashboard service errors
// (ErrNotCourseTeacher等)shared by the handlers below to HTTP status codes.
func (h *Handler) respondTeacherServiceError(c *gin.Context, action, message string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, service.ErrNotCourseTeacher):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrSandboxNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "サンドボックスが見つかりません"})
	default:
		h.respondError(c, http.StatusInternalServerError, action, message, err)
	}
	return true
}

type teacherUnpublishRequest struct {
	CourseID uint      `json:"course_id" binding:"required"`
	UserID   uuid.UUID `json:"user_id" binding:"required"`
}

// TeacherUnpublishSandbox lets the course's teacher force-unpublish one
// specific student's sandbox(POST /program/teacher/unpublish、
// TeacherDashboardModal.tsxの行ごとの「公開停止」ボタン)。
func (h *Handler) TeacherUnpublishSandbox(c *gin.Context) {
	var req teacherUnpublishRequest
	teacherID, ok := h.authorizeTeacherRequest(c, &req)
	if !ok {
		return
	}

	err := h.DB.Transaction(func(tx *gorm.DB) error {
		return service.UnpublishSandboxAsTeacher(tx, teacherID, req.UserID, req.CourseID)
	})
	if h.respondTeacherServiceError(c, "教師ダッシュボード公開停止", "公開の停止に失敗しました", err) {
		return
	}

	c.JSON(http.StatusOK, gin.H{"published": false})
}

type teacherCourseRequest struct {
	CourseID uint `json:"course_id" binding:"required"`
}

// TeacherUnpublishAll unpublishes every currently-published sandbox in the
// course at once(POST /program/teacher/unpublish-all、
// TeacherDashboardModal.tsxの「公開を一括停止」ボタン)。
func (h *Handler) TeacherUnpublishAll(c *gin.Context) {
	var req teacherCourseRequest
	teacherID, ok := h.authorizeTeacherRequest(c, &req)
	if !ok {
		return
	}

	var count int64
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		count, txErr = service.UnpublishAllForCourse(tx, teacherID, req.CourseID)
		return txErr
	})
	if h.respondTeacherServiceError(c, "教師ダッシュボード一括公開停止", "公開の一括停止に失敗しました", err) {
		return
	}

	c.JSON(http.StatusOK, gin.H{"count": count})
}

// TeacherStopAll stops every currently running, non-published sandbox in the
// course at once(POST /program/teacher/stop-all、
// TeacherDashboardModal.tsxの「コンテナ一括停止」ボタン)。公開中のものは
// スキップする(StopAllContainersForCourseのコメント参照) - 呼び出し側は
// 返ってきたskipped_publishedを見て、必要なら先に一括公開停止を促す。
func (h *Handler) TeacherStopAll(c *gin.Context) {
	var req teacherCourseRequest
	teacherID, ok := h.authorizeTeacherRequest(c, &req)
	if !ok {
		return
	}

	result, err := service.StopAllContainersForCourse(c.Request.Context(), h.DockerClient, h.DB, teacherID, req.CourseID)
	if h.respondTeacherServiceError(c, "教師ダッシュボード一括停止", "コンテナの一括停止に失敗しました", err) {
		return
	}

	c.JSON(http.StatusOK, result)
}

type teacherTargetStudentRequest struct {
	CourseID uint      `json:"course_id" binding:"required"`
	UserID   uuid.UUID `json:"user_id" binding:"required"`
}

// TeacherEmergencyStop forcibly kills(docker kill)a specific student's
// running container(POST /program/teacher/emergency-stop、
// TeacherDashboardModal.tsxの「🚨 緊急停止」ボタン、安全対策)。
func (h *Handler) TeacherEmergencyStop(c *gin.Context) {
	var req teacherTargetStudentRequest
	teacherID, ok := h.authorizeTeacherRequest(c, &req)
	if !ok {
		return
	}

	err := h.DB.Transaction(func(tx *gorm.DB) error {
		return service.EmergencyStopContainer(c.Request.Context(), h.DockerClient, tx, teacherID, req.UserID, req.CourseID)
	})
	if h.respondTeacherServiceError(c, "教師ダッシュボード緊急停止", "コンテナの緊急停止に失敗しました", err) {
		return
	}

	c.JSON(http.StatusOK, gin.H{"stopped": true})
}

// TeacherResumeSandbox lets the course's teacher resume a specific student's
// stopped sandbox(POST /program/teacher/resume、TeacherDashboardModal.tsxの
// 「▶ 再開」ボタン) - ロック中でも実行できる(ResumeContainerAsTeacherの
// コメント参照)。
func (h *Handler) TeacherResumeSandbox(c *gin.Context) {
	var req teacherTargetStudentRequest
	teacherID, ok := h.authorizeTeacherRequest(c, &req)
	if !ok {
		return
	}

	var containerID string
	var alreadyRunning bool
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		containerID, alreadyRunning, txErr = service.ResumeContainerAsTeacher(c.Request.Context(), h.DockerClient, tx, teacherID, req.UserID, req.CourseID)
		return txErr
	})
	if h.respondTeacherServiceError(c, "教師ダッシュボード再開", "コンテナの再開に失敗しました", err) {
		return
	}

	status := "running"
	if alreadyRunning {
		status = "already_running"
	}
	c.JSON(http.StatusOK, gin.H{"status": status, "container_id": containerID})
}

type teacherLockRequest struct {
	CourseID uint      `json:"course_id" binding:"required"`
	UserID   uuid.UUID `json:"user_id" binding:"required"`
	Locked   bool      `json:"locked"`
	Reason   string    `json:"reason"`
}

// TeacherSetSandboxLock locks/unlocks a specific student's sandbox against
// being resumed(POST /program/teacher/lock、TeacherDashboardModal.tsxの
// 「🔒 ロック」/「🔓 ロック解除」ボタン、安全対策)。
func (h *Handler) TeacherSetSandboxLock(c *gin.Context) {
	var req teacherLockRequest
	teacherID, ok := h.authorizeTeacherRequest(c, &req)
	if !ok {
		return
	}

	err := h.DB.Transaction(func(tx *gorm.DB) error {
		return service.SetSandboxLocked(tx, teacherID, req.UserID, req.CourseID, req.Locked, req.Reason)
	})
	if h.respondTeacherServiceError(c, "教師ダッシュボードロック設定", "ロック状態の変更に失敗しました", err) {
		return
	}

	c.JSON(http.StatusOK, gin.H{"locked": req.Locked})
}

// IssueTeacherShellTicket lets a course's teacher issue a shell/LSP ticket
// (program_handler.goのIssueShellTicketと同じticketStoreを共有)targeting a
// specific student's sandbox(講師サポート画面のターミナル/LSP接続、
// TeacherLiveSessionModal.tsx等) - IssueShellTicketは呼び出し者自身の
// (userID, courseID)にしか発行できない(authorizeSandboxRequest)ため、
// 講師が生徒のセッションに接続する際は代わりにこちらを使う。チケット自体は
// 誰が発行したかを区別せず(userID, courseID)のみを保持するため、
// service.IssueShellTicketに生徒のUserIDをそのまま渡すだけでよく、
// サービス層の変更は不要。
func (h *Handler) IssueTeacherShellTicket(c *gin.Context) {
	var req teacherTargetStudentRequest
	teacherID, ok := h.authorizeTeacherRequest(c, &req)
	if !ok {
		return
	}

	isCourseTeacher, err := db.IsCourseTeacher(h.DB, req.CourseID, teacherID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "チケット発行", "権限確認に失敗しました", err)
		return
	}
	if !isCourseTeacher {
		c.JSON(http.StatusForbidden, gin.H{"error": service.ErrNotCourseTeacher.Error()})
		return
	}

	ticket, expiresAt, err := service.IssueShellTicket(h.DB, req.UserID, req.CourseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "チケット発行", "チケットの発行に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ticket":     ticket,
		"expires_at": expiresAt,
	})
}
