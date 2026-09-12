package handler

import (
	"errors"
	"net/http"
	"strings"

	"ai-education/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// commitWorkspaceRequest is the body for POST /container/commit
// (「保存(コミット)」ボタンのメッセージ入力から送られる)。
type commitWorkspaceRequest struct {
	CourseID uint   `json:"course_id" binding:"required"`
	Message  string `json:"message" binding:"required"`
}

// CommitWorkspace は生徒コンテナのworkspace全体をgitコミットする
// (「保存(コミット)」ボタン、NextPlan.md フェーズ5)。
func (h *Handler) CommitWorkspace(c *gin.Context) {
	var req commitWorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コミットメッセージを入力してください"})
		return
	}

	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}

	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, req.CourseID)
	if !ok {
		return
	}

	err := service.CommitWorkspace(c.Request.Context(), h.DockerClient, containerID, req.Message)
	switch {
	case errors.Is(err, service.ErrNothingToCommit):
		c.JSON(http.StatusConflict, gin.H{"error": "コミットする変更がありません"})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "コミット", "変更の保存(コミット)に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "保存しました"})
}

// gitHistoryQuery is the query string for GET /sandbox/git/history.
type gitHistoryQuery struct {
	CourseID uint `form:"course_id" binding:"required"`
}

// GitHistory は「変更履歴」タブのHistoryListが表示するコミット一覧
// (ハッシュ・作者・メッセージ・日時)を返す。
func (h *Handler) GitHistory(c *gin.Context) {
	var req gitHistoryQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDは必須です"})
		return
	}

	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}
	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, req.CourseID)
	if !ok {
		return
	}

	commits, err := service.GitHistory(c.Request.Context(), h.DockerClient, containerID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "変更履歴取得", "変更履歴の取得に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"commits": commits})
}

// GitStatus は「ソース管理」パネルの変更ファイル一覧(git status)を返す。
// 講師・生徒どちらの画面から呼ばれても同じコンテナのworkspaceへ都度
// 問い合わせるだけなので、常に最新の実態を返す(authorizeSandboxCourseの
// as_user上書き、file.go参照 - 特別な同期メッセージ等は不要)。
func (h *Handler) GitStatus(c *gin.Context) {
	var req gitHistoryQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDは必須です"})
		return
	}

	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}
	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, req.CourseID)
	if !ok {
		return
	}

	entries, err := service.GitStatus(c.Request.Context(), h.DockerClient, containerID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "変更状態取得", "変更状態の取得に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"files": entries})
}

// gitDiffQuery is the query string for GET /sandbox/git/diff/:hash. Path is
// optional - the file within the commit that DiffViewer currently has
// selected; omitted (or on first load) defaults server-side to the commit's
// first changed file.
type gitDiffQuery struct {
	CourseID uint   `form:"course_id" binding:"required"`
	Path     string `form:"path"`
}

// GitDiff は指定コミットのメタ情報・変更ファイル一覧・(選択中ファイルの)
// 変更前後の全文を返す。DiffViewerはoriginal/modifiedをそのままMonacoの
// DiffEditorへ渡すだけでよい(unified diffのパースは不要)。
func (h *Handler) GitDiff(c *gin.Context) {
	hash := strings.TrimSpace(c.Param("hash"))
	if hash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コミットハッシュは必須です"})
		return
	}

	var req gitDiffQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDは必須です"})
		return
	}

	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}
	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, req.CourseID)
	if !ok {
		return
	}

	diff, err := service.GitDiff(c.Request.Context(), h.DockerClient, containerID, hash, req.Path)
	switch {
	case errors.Is(err, service.ErrCommitNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "指定されたコミットが見つかりません"})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "差分取得", "差分の取得に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, diff)
}

// revertWorkspaceRequest is the body for POST /sandbox/git/revert
// (RevertButtonの確認ダイアログ確定後に送られる)。
type revertWorkspaceRequest struct {
	CourseID   uint   `json:"course_id" binding:"required"`
	CommitHash string `json:"commit_hash" binding:"required"`
}

// RevertWorkspace はワークスペースを指定コミットの状態まで巻き戻す
// (「この状態に巻き戻す」ボタン)。既存の履歴は壊さず、巻き戻し自体を新しい
// コミットとして記録する(RevertWorkspaceTo参照)。
func (h *Handler) RevertWorkspace(c *gin.Context) {
	var req revertWorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.CommitHash) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}

	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}
	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, req.CourseID)
	if !ok {
		return
	}

	err := service.RevertWorkspaceTo(c.Request.Context(), h.DockerClient, containerID, req.CommitHash)
	switch {
	case errors.Is(err, service.ErrCommitNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "指定されたコミットが見つかりません"})
		return
	case errors.Is(err, service.ErrNothingToRevert):
		c.JSON(http.StatusConflict, gin.H{"error": "既にこの状態です"})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "巻き戻し", "指定した状態への復元に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "指定した状態に戻しました"})
}
