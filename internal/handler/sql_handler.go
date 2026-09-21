package handler

import (
	"net/http"
	"strings"

	"ai-education/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// executeSandboxSQLRequest is the body for POST /program/container/sql/execute
// (「マルチDB対応SQL実行&閲覧UI」作業指示書)。指示書は db_type
// ("sqlite"|"postgres"|"mysql")を含む3DB共通の形だが、今回はSQLiteのみを
// 実装する(既存のsandbox-net分離設計上、backendから直接Postgres/MySQLへ
// ネットワーク接続することがそもそも技術的に成立しないため、CLIをexec経由
// で叩く方式でDBごとに個別実装が必要 - Postgres/MySQLは合意の上で今回は
// 見送り)。db_typeは将来の拡張に備えて受け取るが、"sqlite"以外は明示的に
// 400を返す。
type executeSandboxSQLRequest struct {
	CourseID uint   `json:"course_id" binding:"required"`
	DBType   string `json:"db_type"`
	DBTarget string `json:"db_target"`
	Query    string `json:"query" binding:"required"`
}

// ExecuteSandboxSQL executes a single SQL statement against a SQLite
// database file inside the caller's own sandbox workspace(サイドバーの
// テーブル一覧取得も、SQLエディタからの自由なSQL実行も、どちらもこの
// 1つのエンドポイントを叩く - テーブル一覧はフロント側が
// `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE
// 'sqlite_%';`を発行するだけの、普通のSELECTの一種として扱う)。
func (h *Handler) ExecuteSandboxSQL(c *gin.Context) {
	var req executeSandboxSQLRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Query) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}

	dbType := req.DBType
	if dbType == "" {
		dbType = "sqlite"
	}
	if dbType != "sqlite" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "現在サポートされているDB種別はsqliteのみです"})
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

	result, err := service.ExecuteSandboxSQLite(c.Request.Context(), h.DockerClient, containerID, req.DBTarget, req.Query)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "SQL実行", "SQLの実行に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, result)
}
