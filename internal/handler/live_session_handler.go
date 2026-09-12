package handler

import (
	"errors"
	"net/http"
	"strings"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// 講師による生徒セッションのリアルタイム監視・共同操作(ライブセッション
// 同期、Pair Programming / Live Share形式)。ワイヤ上は既存のシェル/LSP/
// プレビューと同じ「短命ワンタイムチケット + AuthMiddlewareの外の
// WebSocket」という形(ブラウザのWebSocket APIがAuthorizationヘッダーを
// 付けられないため)。チケット発行(このファイルの2つのIssue*)はPASETO
// 認証済みの通常APIとして/programグループの下に置き、実際の共有処理
// (JoinShellHub/JoinEditorHub)はinternal/service/live_session_hub.goに
// 委譲する。

// IssueOwnLiveSessionTicket lets a student issue a ticket to join their own
// live session(POST /program/container/live-session-ticket)。生徒自身の
// ワークスペース(WorkspaceLayout.tsx)がマウント時に常時これを呼び、
// シェル/エディタの両WebSocketへ接続しておく - こうしておかないと、
// 講師が後から参加してきた時に「生徒側で誰も繋がっていない」状態になり
// 得るため(講師が最初の接続者になっても動作はするが、生徒のバナー表示
// 用WebSocketが無いとプレゼンス通知を受け取れない)。
func (h *Handler) IssueOwnLiveSessionTicket(c *gin.Context) {
	userID, courseID, ok := h.authorizeSandboxRequest(c)
	if !ok {
		return
	}

	name := h.resolveDisplayName(c, userID)
	ticket, expiresAt, err := service.IssueLiveTicket(userID, courseID, service.LiveSessionRoleStudent, name)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "ライブセッションチケット発行", "チケットの発行に失敗しました", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": ticket, "expires_at": expiresAt})
}

type teacherLiveTicketRequest struct {
	CourseID uint      `json:"course_id" binding:"required"`
	UserID   uuid.UUID `json:"user_id" binding:"required"`
}

// IssueTeacherLiveSessionTicket lets a course's teacher issue a ticket to
// join a specific student's live session(POST /program/teacher/live-session-ticket、
// TeacherDashboardModal.tsxの「セッションに参加」ボタン)。
func (h *Handler) IssueTeacherLiveSessionTicket(c *gin.Context) {
	var req teacherLiveTicketRequest
	teacherID, ok := h.authorizeTeacherRequest(c, &req)
	if !ok {
		return
	}

	isTeacher, err := db.IsCourseTeacher(h.DB, req.CourseID, teacherID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "ライブセッションチケット発行", "権限確認に失敗しました", err)
		return
	}
	if !isTeacher {
		c.JSON(http.StatusForbidden, gin.H{"error": service.ErrNotCourseTeacher.Error()})
		return
	}

	name := h.resolveDisplayName(c, teacherID)
	ticket, expiresAt, err := service.IssueLiveTicket(req.UserID, req.CourseID, service.LiveSessionRoleTeacher, name)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "ライブセッションチケット発行", "チケットの発行に失敗しました", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": ticket, "expires_at": expiresAt})
}

type peerLiveTicketRequest struct {
	CourseID uint      `json:"course_id" binding:"required"`
	UserID   uuid.UUID `json:"user_id" binding:"required"`
}

// IssuePeerLiveSessionTicket lets a fellow enrolled student(相互閲覧
// (Peer Viewer)機能の閲覧者)issue a ticket to join a specific classmate's
// live session as a read-only viewer(POST /program/peer/live-session-ticket、
// ClassmatesPanel.tsxのカードクリック)。担当教師かどうかは問わない - 同じ
// クラスに参加している生徒同士であれば誰でも閲覧できる(相互学習用ライブ
// ビューアーという機能の性質上)。閲覧対象(req.UserID)も同じクラスに
// 参加していることを確認する - 課題IDだけを頼りに任意のUUIDを渡されても、
// 無関係な生徒のセッションを覗けないようにするため。
func (h *Handler) IssuePeerLiveSessionTicket(c *gin.Context) {
	var req peerLiveTicketRequest
	viewerID, authed := utils.GetUserID(c)
	if !authed {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}

	isViewerEnrolled, err := db.IsStudentInCourse(h.DB, viewerID, req.CourseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "ライブセッションチケット発行", "コース所属確認に失敗しました", err)
		return
	}
	if !isViewerEnrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "このクラスには参加していません"})
		return
	}
	isTargetEnrolled, err := db.IsStudentInCourse(h.DB, req.UserID, req.CourseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "ライブセッションチケット発行", "コース所属確認に失敗しました", err)
		return
	}
	if !isTargetEnrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "指定された生徒はこのクラスに参加していません"})
		return
	}

	name := h.resolveDisplayName(c, viewerID)
	ticket, expiresAt, err := service.IssueLiveTicket(req.UserID, req.CourseID, service.LiveSessionRolePeer, name)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "ライブセッションチケット発行", "チケットの発行に失敗しました", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": ticket, "expires_at": expiresAt})
}

// resolveDisplayName は表示用の短い名前を取り出す(users.nameの
// ハイフン以降は一意性確保のためのサフィックスなので落とす、
// ProgramContainerCard.StudentNameと同じ切り出し方)。取得に失敗しても
// チケット発行自体は止めない(バナーの表示名が空になるだけ)。
func (h *Handler) resolveDisplayName(c *gin.Context, userID uuid.UUID) string {
	user, err := db.FindUserByID(h.DB, userID.String())
	if err != nil {
		return ""
	}
	return strings.SplitN(user.Name, "-", 2)[0]
}

// liveUpgrader upgrades to a WebSocket for ライブセッション同期。
// shellUpgrader(shell_handler.go)と同じ理由でCheckOriginは許可制ではなく
// チケットで認可する。
var liveUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// LiveShellWebSocket upgrades to a WebSocket and joins the shared pty for
// the ticket's target student session(GET /program/container/live/shell?ticket=...)。
func (h *Handler) LiveShellWebSocket(c *gin.Context) {
	ticketID := c.Query("ticket")
	if ticketID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "チケットが指定されていません"})
		return
	}
	ticket, ok := service.ConsumeLiveTicket(ticketID)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "チケットが無効か期限切れです"})
		return
	}
	// 生徒間でのリアルタイム相互閲覧(Peer Viewer)は読み取り専用 - ターミナル
	// (stdin送信)には参加させない(タスク要件の「他生徒からのターミナル
	// stdin送信リクエストは403 Forbiddenでブロック」に対応)。フロント側は
	// そもそもクラスメイト閲覧モードでこのエンドポイントを叩かない設計だが、
	// チケットのRoleで発行元(IssuePeerLiveSessionTicket)が固定するため、
	// サーバー側でも確実に弾ける。
	if ticket.Role == service.LiveSessionRolePeer {
		c.JSON(http.StatusForbidden, gin.H{"error": "クラスメイトはターミナルを操作できません"})
		return
	}

	ctx := c.Request.Context()
	containerID, err := service.ContainerIDForSession(ctx, h.DockerClient, ticket.TargetUserID, ticket.CourseID)
	if err != nil {
		status, msg := http.StatusInternalServerError, "サンドボックスへの接続に失敗しました"
		switch {
		case errors.Is(err, service.ErrSandboxNotFound):
			status, msg = http.StatusNotFound, "サンドボックスが見つかりません"
		case errors.Is(err, service.ErrSandboxNotRunning):
			status, msg = http.StatusConflict, "サンドボックスが起動していません"
		}
		c.JSON(status, gin.H{"error": msg})
		return
	}

	conn, err := liveUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	_ = service.JoinShellHub(ctx, h.DockerClient, containerID, ticket.TargetUserID, ticket.CourseID, conn, ticket.Role, ticket.DisplayName)
}

// LiveEditorWebSocket upgrades to a WebSocket and joins the pure-relay
// editor channel for the ticket's target student session
// (GET /program/container/live/editor?ticket=...)。Dockerとのやり取りは
// 一切無い(JoinShellHubと違いptyを持たない)。
func (h *Handler) LiveEditorWebSocket(c *gin.Context) {
	ticketID := c.Query("ticket")
	if ticketID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "チケットが指定されていません"})
		return
	}
	ticket, ok := service.ConsumeLiveTicket(ticketID)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "チケットが無効か期限切れです"})
		return
	}

	conn, err := liveUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	service.JoinEditorHub(ticket.TargetUserID, ticket.CourseID, conn, ticket.Role, ticket.DisplayName)
}
