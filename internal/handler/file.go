package handler

import (
	"errors"
	"net/http"
	"path"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// sandboxFileQuery is the query-string binding shared by the read-only file
// browsing endpoints below. Unlike the JSON-body POST endpoints in
// program_handler.go, these are GETs (list/read have no side effects), so
// course_id/path are ordinary query params rather than a request body.
type sandboxFileQuery struct {
	CourseID uint `form:"course_id" binding:"required"`
}

// resolveTeacherActingAs backs the ?as_user=<uuid> query-param override
// honored by authorizeSandboxFileQuery/authorizeSandboxCourse below -
// 講師サポート画面(TeacherLiveSessionModal等)からGit/ファイル/公開操作を
// 生徒本人の代わりに行うためのもの。callerIDがこのコースの担当教師である
// とDB確認できた場合のみasUserを対象userIDとして採用する(生徒がオフラインで
// も操作できるよう、生徒自身のIsStudentInCourseチェックは経由しない)。
func (h *Handler) resolveTeacherActingAs(c *gin.Context, callerID uuid.UUID, courseID uint, asUser string) (targetID uuid.UUID, ok bool) {
	targetID, err := uuid.Parse(asUser)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "as_userが不正です"})
		return uuid.Nil, false
	}
	isTeacher, teacherOk := utils.GetUserTeacher(c)
	if !teacherOk || !isTeacher {
		c.JSON(http.StatusForbidden, gin.H{"error": "先生以外はこの操作を行えません"})
		return uuid.Nil, false
	}
	isCourseTeacher, err := db.IsCourseTeacher(h.DB, courseID, callerID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス操作", "権限確認に失敗しました", err)
		return uuid.Nil, false
	}
	if !isCourseTeacher {
		c.JSON(http.StatusForbidden, gin.H{"error": service.ErrNotCourseTeacher.Error()})
		return uuid.Nil, false
	}
	return targetID, true
}

// resolveViewerActingAs backs the ?as_user=<uuid> query-param override
// honored by authorizeSandboxFileQuery(read-only GETのみ) - resolveTeacherActingAs
// と違い、担当教師だけでなく「同じクラスに参加している他の生徒(相互閲覧/
// Peer Viewer機能の閲覧者)」も対象として認める。書き込み系
// (authorizeSandboxCourse)は引き続きresolveTeacherActingAsのみを使う
// (教師のみ)ため、他生徒からのファイル変更(POST/PUT/PATCH/DELETE)・Git・
// 公開操作は常に403のまま - このリゾルバは読み取り専用エンドポイント
// (ListSandboxFiles/GetSandboxFileContent/GetSandboxPublishStatus)にしか
// 使われないことで、書き込み経路を一切開かない設計にしている。
func (h *Handler) resolveViewerActingAs(c *gin.Context, callerID uuid.UUID, courseID uint, asUser string) (targetID uuid.UUID, ok bool) {
	targetID, err := uuid.Parse(asUser)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "as_userが不正です"})
		return uuid.Nil, false
	}

	if isTeacher, teacherOk := utils.GetUserTeacher(c); teacherOk && isTeacher {
		isCourseTeacher, err := db.IsCourseTeacher(h.DB, courseID, callerID)
		if err != nil {
			h.respondError(c, http.StatusInternalServerError, "サンドボックス操作", "権限確認に失敗しました", err)
			return uuid.Nil, false
		}
		if isCourseTeacher {
			return targetID, true
		}
		// このコースの担当教師ではない先生アカウント - 先生も同時に別クラス
		// では生徒として参加し得るため、ここでは403にせず下のクラスメイト
		// 判定へフォールスルーする(該当しなければ結局403になる)。
	}

	isCallerEnrolled, err := db.IsStudentInCourse(h.DB, callerID, courseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス操作", "コース所属確認に失敗しました", err)
		return uuid.Nil, false
	}
	if !isCallerEnrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "このクラスには参加していません"})
		return uuid.Nil, false
	}
	isTargetEnrolled, err := db.IsStudentInCourse(h.DB, targetID, courseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス操作", "コース所属確認に失敗しました", err)
		return uuid.Nil, false
	}
	if !isTargetEnrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "指定された生徒はこのクラスに参加していません"})
		return uuid.Nil, false
	}
	return targetID, true
}

// authorizeSandboxFileQuery mirrors authorizeSandboxRequest
// (program_handler.go) but binds course_id from the query string instead of
// a JSON body. Also honors ?as_user=<uuid> (resolveViewerActingAs) so the
// course's teacher, or a fellow enrolled classmate (相互閲覧/Peer Viewer),
// can browse a specific student's files read-only - the student's own
// IsStudentInCourse check below is skipped entirely in that case.
func (h *Handler) authorizeSandboxFileQuery(c *gin.Context) (userID uuid.UUID, courseID uint, ok bool) {
	uid, authed := utils.GetUserID(c)
	if !authed {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return uid, 0, false
	}

	var req sandboxFileQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDは必須です"})
		return uid, 0, false
	}

	if asUser := c.Query("as_user"); asUser != "" {
		targetID, viewerOk := h.resolveViewerActingAs(c, uid, req.CourseID, asUser)
		if !viewerOk {
			return uid, 0, false
		}
		return targetID, req.CourseID, true
	}

	isJoined, err := db.IsStudentInCourse(h.DB, uid, req.CourseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス操作", "コース所属確認に失敗しました", err)
		return uid, 0, false
	}
	if !isJoined {
		c.JSON(http.StatusForbidden, gin.H{"error": "このクラスには参加していません"})
		return uid, 0, false
	}

	return uid, req.CourseID, true
}

// authorizeSandboxCourse checks the caller is authenticated and enrolled in
// courseID. Shared by the mutation endpoints below (create/move/delete),
// which each bind their own JSON body (with extra fields beyond course_id)
// before calling this - unlike authorizeSandboxRequest/
// authorizeSandboxFileQuery's fixed {course_id}-only shape, so this takes
// courseID as a plain argument instead of binding it itself. Also honors
// ?as_user=<uuid> (resolveTeacherActingAs above), same as
// authorizeSandboxFileQuery.
func (h *Handler) authorizeSandboxCourse(c *gin.Context, courseID uint) (userID uuid.UUID, ok bool) {
	uid, authed := utils.GetUserID(c)
	if !authed {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return uid, false
	}

	if asUser := c.Query("as_user"); asUser != "" {
		targetID, teacherOk := h.resolveTeacherActingAs(c, uid, courseID, asUser)
		if !teacherOk {
			return uid, false
		}
		return targetID, true
	}

	isJoined, err := db.IsStudentInCourse(h.DB, uid, courseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス操作", "コース所属確認に失敗しました", err)
		return uid, false
	}
	if !isJoined {
		c.JSON(http.StatusForbidden, gin.H{"error": "このクラスには参加していません"})
		return uid, false
	}

	return uid, true
}

// sandboxContainerIDOrRespond resolves the caller's running sandbox
// container, writing the appropriate error response itself (matching
// ShellWebSocket's status mapping in shell_handler.go) when it isn't
// available, so callers can just check ok.
func (h *Handler) sandboxContainerIDOrRespond(c *gin.Context, userID uuid.UUID, courseID uint) (containerID string, ok bool) {
	containerID, err := service.ContainerIDForSession(c.Request.Context(), h.DockerClient, userID, courseID)
	if err == nil {
		return containerID, true
	}
	status, msg := http.StatusInternalServerError, "サンドボックスへの接続に失敗しました"
	switch {
	case errors.Is(err, service.ErrSandboxNotFound):
		status, msg = http.StatusNotFound, "サンドボックスが見つかりません"
	case errors.Is(err, service.ErrSandboxNotRunning):
		status, msg = http.StatusConflict, "サンドボックスが起動していません。再開してから接続してください"
	}
	c.JSON(status, gin.H{"error": msg})
	return "", false
}

// ListSandboxFiles は生徒コンテナのworkspace配下(既定 /root/workspace)を
// 1階層分だけ返す。フロントのファイルツリーはディレクトリを開くたびにこれを
// 呼び、必要な階層だけを遅延取得する(NextPlan.md フェーズ4)。
func (h *Handler) ListSandboxFiles(c *gin.Context) {
	userID, courseID, ok := h.authorizeSandboxFileQuery(c)
	if !ok {
		return
	}

	dirPath, err := service.ResolveSandboxPath(c.Query("path"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "パスが不正です"})
		return
	}

	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, courseID)
	if !ok {
		return
	}

	items, err := service.ListSandboxFiles(c.Request.Context(), h.DockerClient, containerID, dirPath)
	if errors.Is(err, service.ErrSandboxPathNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "指定されたパスが見つかりません"})
		return
	}
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "ファイル一覧取得", "ファイル一覧の取得に失敗しました", err)
		return
	}
	if items == nil {
		items = []service.SandboxFileEntry{}
	}

	c.JSON(http.StatusOK, gin.H{"path": dirPath, "items": items})
}

// GetSandboxFileContent は指定したファイル1つのテキスト内容を返す
// (フロントのMonaco Editorがファイルクリック時に呼び出す)。
func (h *Handler) GetSandboxFileContent(c *gin.Context) {
	userID, courseID, ok := h.authorizeSandboxFileQuery(c)
	if !ok {
		return
	}

	reqPath := c.Query("path")
	if reqPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pathは必須です"})
		return
	}
	filePath, err := service.ResolveSandboxPath(reqPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "パスが不正です"})
		return
	}

	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, courseID)
	if !ok {
		return
	}

	content, err := service.GetSandboxFileContent(c.Request.Context(), h.DockerClient, containerID, filePath)
	switch {
	case errors.Is(err, service.ErrSandboxPathNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "ファイルが見つかりません"})
		return
	case errors.Is(err, service.ErrSandboxPathIsDirectory):
		c.JSON(http.StatusBadRequest, gin.H{"error": "指定されたパスはディレクトリです"})
		return
	case errors.Is(err, service.ErrSandboxFileTooLarge):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "ファイルサイズが大きすぎるため表示できません"})
		return
	case errors.Is(err, service.ErrSandboxFileNotText):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "テキストファイルではないため表示できません"})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "ファイル取得", "ファイル内容の取得に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"path": filePath, "content": content})
}

// WriteSandboxFileContent は指定したファイル1つの内容を書き換える(作成も兼ねる)
// (フロントのMonaco Editorの保存(Ctrl/Cmd+S)から呼び出される)。
func (h *Handler) WriteSandboxFileContent(c *gin.Context) {
	var req writeSandboxFileContentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}
	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}

	targetPath, err := service.ResolveSandboxPath(req.Path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "パスが不正です"})
		return
	}
	if service.IsSandboxRoot(targetPath) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workspaceルート自体には書き込めません"})
		return
	}

	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, req.CourseID)
	if !ok {
		return
	}

	err = service.WriteSandboxFileContent(c.Request.Context(), h.DockerClient, containerID, targetPath, req.Content)
	switch {
	case errors.Is(err, service.ErrSandboxPathIsDirectory):
		c.JSON(http.StatusBadRequest, gin.H{"error": "指定されたパスはディレクトリです"})
		return
	case errors.Is(err, service.ErrSandboxPathNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "保存先の親フォルダが見つかりません"})
		return
	case errors.Is(err, service.ErrSandboxFileTooLarge):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "ファイルサイズが大きすぎます"})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "ファイル保存", "ファイルの保存に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"path": targetPath})
}

// writeSandboxFileContentRequest is the body for PATCH /container/file
// (save content - Monaco Editor's save).
type writeSandboxFileContentRequest struct {
	CourseID uint   `json:"course_id" binding:"required"`
	Path     string `json:"path" binding:"required"`
	Content  string `json:"content"`
}

// createSandboxPathRequest is the body for POST /container/file (create).
type createSandboxPathRequest struct {
	CourseID uint   `json:"course_id" binding:"required"`
	Path     string `json:"path" binding:"required"`
	IsDir    bool   `json:"is_dir"`
}

// moveSandboxPathRequest is the body for PUT /container/file (rename/move -
// the same operation covers both: a rename is just a move within the same
// parent directory).
type moveSandboxPathRequest struct {
	CourseID uint   `json:"course_id" binding:"required"`
	OldPath  string `json:"old_path" binding:"required"`
	NewPath  string `json:"new_path" binding:"required"`
}

// deleteSandboxPathRequest is the body for DELETE /container/file. Like
// DeleteProgramContainer (program_handler.go), a DELETE with a JSON body is
// already this app's established pattern.
type deleteSandboxPathRequest struct {
	CourseID uint   `json:"course_id" binding:"required"`
	Path     string `json:"path" binding:"required"`
}

// CreateSandboxPath は生徒コンテナのworkspace配下に新規の空ファイル/フォルダを
// 作成する(NextPlan.md フェーズ4「ファイル・フォルダーのCRUD操作実装」)。
func (h *Handler) CreateSandboxPath(c *gin.Context) {
	var req createSandboxPathRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}
	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}

	targetPath, err := service.ResolveSandboxPath(req.Path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "パスが不正です"})
		return
	}

	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, req.CourseID)
	if !ok {
		return
	}

	err = service.CreateSandboxPath(c.Request.Context(), h.DockerClient, containerID, targetPath, req.IsDir)
	switch {
	case errors.Is(err, service.ErrSandboxPathAlreadyExists):
		c.JSON(http.StatusConflict, gin.H{"error": "既に存在します"})
		return
	case errors.Is(err, service.ErrSandboxPathNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "作成先の親フォルダが見つかりません"})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "ファイル作成", "ファイル/フォルダの作成に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": service.SandboxFileEntry{Name: path.Base(targetPath), Path: targetPath, IsDir: req.IsDir, Size: 0},
	})
}

// MoveSandboxPath は生徒コンテナのworkspace配下のファイル/フォルダをリネーム
// または移動する(NextPlan.md フェーズ4「ファイル・フォルダーのCRUD操作実装」。
// リネームは同じ親ディレクトリ内への移動として同じロジックで扱う)。
func (h *Handler) MoveSandboxPath(c *gin.Context) {
	var req moveSandboxPathRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}
	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}

	oldPath, err := service.ResolveSandboxPath(req.OldPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "移動元のパスが不正です"})
		return
	}
	newPath, err := service.ResolveSandboxPath(req.NewPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "移動先のパスが不正です"})
		return
	}
	if service.IsSandboxRoot(oldPath) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workspaceルート自体は移動できません"})
		return
	}

	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, req.CourseID)
	if !ok {
		return
	}

	err = service.MoveSandboxPath(c.Request.Context(), h.DockerClient, containerID, oldPath, newPath)
	switch {
	case errors.Is(err, service.ErrSandboxPathAlreadyExists):
		c.JSON(http.StatusConflict, gin.H{"error": "移動先に既に存在します"})
		return
	case errors.Is(err, service.ErrSandboxPathNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "移動元が見つかりません"})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "ファイル移動", "ファイル/フォルダの移動に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"path": newPath})
}

// DeleteSandboxPath は生徒コンテナのworkspace配下のファイル、またはフォルダと
// その中身をまとめて削除する(NextPlan.md フェーズ4「ファイル・フォルダーの
// CRUD操作実装」)。
func (h *Handler) DeleteSandboxPath(c *gin.Context) {
	var req deleteSandboxPathRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}
	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}

	targetPath, err := service.ResolveSandboxPath(req.Path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "パスが不正です"})
		return
	}
	if service.IsSandboxRoot(targetPath) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workspaceルート自体は削除できません"})
		return
	}

	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, req.CourseID)
	if !ok {
		return
	}

	err = service.DeleteSandboxPath(c.Request.Context(), h.DockerClient, containerID, targetPath)
	switch {
	case errors.Is(err, service.ErrSandboxPathNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "指定されたパスが見つかりません"})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "ファイル削除", "ファイル/フォルダの削除に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "削除しました"})
}
