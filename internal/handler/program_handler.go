package handler

import (
	"errors"
	"log"
	"net/http"
	"unicode/utf8"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// sandboxNameMaxLength は model.ProgramSandbox.Name の列サイズ(varchar(100))と
// 一致させる。これを超える名前はDB挿入時に「value too long for type character
// varying(100)」で失敗するが、そのタイミングは既にDockerコンテナを起動済みの後
// (StartProgramContainer)なので、事前にここで弾いて無駄なコンテナ起動/削除
// サイクルと「コンテナの記録に失敗しました」の誤解を招くエラーを避ける。
const sandboxNameMaxLength = 100

// programContainerRequest is the shared request body for resume/stop/delete/list:
// 画像分類システムのAiCard()等と同様、クラス(course_id)単位でリソースを
// 紐付ける(1ユーザーにつき1クラス1環境)。
type programContainerRequest struct {
	CourseID uint `json:"course_id" binding:"required"`
}

// createContainerRequest is the body for creating a brand-new sandbox: adds
// the student-chosen display name (optional - falls back server-side).
type createContainerRequest struct {
	CourseID uint   `json:"course_id" binding:"required"`
	Name     string `json:"name"`
}

// authorizeSandboxRequest binds course_id, verifies auth, and checks course
// membership (同じチェックをAiCard()等の既存ハンドラーでも行っている)。
// Returns (userID, courseID, ok) - ok=false means the handler already wrote
// a response and the caller must return immediately.
func (h *Handler) authorizeSandboxRequest(c *gin.Context) (userID uuid.UUID, courseID uint, ok bool) {
	uid, authed := utils.GetUserID(c)
	if !authed {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return uid, 0, false
	}

	var req programContainerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDは必須です"})
		return uid, 0, false
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

// authorizeCreateRequest is like authorizeSandboxRequest but also binds the
// student-chosen display name for a brand-new sandbox.
func (h *Handler) authorizeCreateRequest(c *gin.Context) (userID uuid.UUID, courseID uint, name string, ok bool) {
	uid, authed := utils.GetUserID(c)
	if !authed {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return uid, 0, "", false
	}

	var req createContainerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "コースIDは必須です"})
		return uid, 0, "", false
	}
	if utf8.RuneCountInString(req.Name) > sandboxNameMaxLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "環境の名前は100文字以内で入力してください", "retryable": false})
		return uid, 0, "", false
	}

	isJoined, err := db.IsStudentInCourse(h.DB, uid, req.CourseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス操作", "コース所属確認に失敗しました", err)
		return uid, 0, "", false
	}
	if !isJoined {
		c.JSON(http.StatusForbidden, gin.H{"error": "このクラスには参加していません"})
		return uid, 0, "", false
	}

	return uid, req.CourseID, req.Name, true
}

// IssueShellTicket は、既にPASETO認証済みのセッションから、シェル/LSP用
// WebSocket接続向けの短命ワンタイムチケットを発行する(NextPlan.md §3.2)。
// 長期間有効なPASETOトークンをWebSocketのURLクエリにそのまま載せるとアクセス
// ログに残るリスクがあるため、有効期限30秒・使い捨てのチケットを別途発行し、
// WebSocket接続時にはこのチケットのみを使う設計とする。
// (このチケットを検証・消費する側のWebSocketエンドポイント(シェル/LSP中継)は
// NextPlan.md フェーズ3・6で別途実装予定、本エンドポイントの時点では未実装)。
func (h *Handler) IssueShellTicket(c *gin.Context) {
	userID, courseID, ok := h.authorizeSandboxRequest(c)
	if !ok {
		return
	}

	ticket, expiresAt, err := service.IssueShellTicket(h.DB, userID, courseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "チケット発行", "チケットの発行に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ticket":     ticket,
		"expires_at": expiresAt,
	})
}

// ListProgramContainers はクラス内の全生徒のサンドボックス一覧を返す
// (画像分類AIの「みんなが作ったAIモデル」一覧と同様、クラスメンバー全員が閲覧可能)。
// 各カードに is_mine を付与し、フロントが「自分のコンテナ」にだけ操作ボタン
// (停止/削除)を出せるようにする(/api/v1/user はユーザーIDを返さないため)。
func (h *Handler) ListProgramContainers(c *gin.Context) {
	userID, courseID, ok := h.authorizeSandboxRequest(c)
	if !ok {
		return
	}

	cards, err := db.ListProgramSandboxesForCourse(h.DB, courseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス一覧取得", "コンテナ一覧の取得に失敗しました", err)
		return
	}
	for i := range cards {
		cards[i].IsMine = cards[i].UserID == userID
		// 単一サブドメイン公開機能(NextPlan.md フェーズ7):カード一覧から
		// 直接「Webサイトを開く」ためのURLを組み立てる(publishURL、
		// publish_handler.go)。非公開のカードはPublishSlugが空文字列のまま
		// なので、組み立てても意味の無い値にはなるが、フロント側は必ず
		// published===trueの時だけこの値を使う想定。
		if cards[i].Published {
			cards[i].PublishURL = publishURL(cards[i].PublishSlug)
		}
	}

	c.JSON(http.StatusOK, gin.H{"containers": cards})
}

// StartProgramContainer は認証ユーザーの学習用コンテナを新規作成して起動する。
// 既に(起動中/停止中を問わず)コンテナが存在する場合は409を返す
// (先に削除してから作成し直すよう促す。再開は ResumeProgramContainer を使う)。
func (h *Handler) StartProgramContainer(c *gin.Context) {
	userID, courseID, name, ok := h.authorizeCreateRequest(c)
	if !ok {
		return
	}

	containerID, err := service.StartProgramContainer(c.Request.Context(), h.DockerClient, h.DB, userID, courseID, name)
	if errors.Is(err, service.ErrSandboxAlreadyExists) {
		c.JSON(http.StatusConflict, gin.H{"error": "既にコンテナが存在します。作成するには先に削除してください。", "retryable": false})
		return
	}
	if errors.Is(err, service.ErrSandboxPoolExhausted) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "現在利用できる環境の上限に達しています。しばらくしてからもう一度お試しください。", "retryable": true})
		return
	}
	// StartContainerErrorはDocker起動(自動リトライ済み)・記録失敗のいずれかを
	// 生徒向けメッセージ+再試行の可否に分類したもの(NextPlan.md フェーズ2
	// 「コンテナ起動失敗時のリトライ・ユーザー通知フローを設計」)。
	var startErr *service.StartContainerError
	if errors.As(err, &startErr) {
		log.Printf("[サンドボックス作成] %s retryable=%v: %v", startErr.Message, startErr.Retryable, startErr.Cause)
		if saveErr := db.SaveSystemLog(h.DB, "ERROR", "サンドボックス作成", startErr.Message, startErr.Cause.Error(), nil); saveErr != nil {
			log.Printf("[エラーログ保存失敗] %v", saveErr)
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": startErr.Message, "retryable": startErr.Retryable})
		return
	}
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス作成", "コンテナの作成に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"container_id": containerID,
		"status":       "running",
	})
}

// ResumeProgramContainer は認証ユーザーの既存(停止中)の学習用コンテナを再開する。
// コンテナが存在しない場合は404を返す(作成するには StartProgramContainer を使う)。
func (h *Handler) ResumeProgramContainer(c *gin.Context) {
	userID, courseID, ok := h.authorizeSandboxRequest(c)
	if !ok {
		return
	}

	containerID, alreadyRunning, err := service.ResumeProgramContainer(c.Request.Context(), h.DockerClient, h.DB, userID, courseID)
	if errors.Is(err, service.ErrSandboxNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "サンドボックスが見つかりません"})
		return
	}
	if errors.Is(err, service.ErrSandboxLocked) {
		// retryable:false - 教師がロックを解除しない限り、再試行しても
		// 同じ理由で必ず失敗するため(ErrSandboxPublishedと同じ方針)。
		c.JSON(http.StatusConflict, gin.H{"error": service.ErrSandboxLocked.Error(), "retryable": false})
		return
	}
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス再開", "コンテナの再開に失敗しました", err)
		return
	}

	status := "running"
	if alreadyRunning {
		status = "already_running"
	}
	c.JSON(http.StatusOK, gin.H{
		"container_id": containerID,
		"status":       status,
	})
}

// StopProgramContainer は認証ユーザーの学習用コンテナを停止する。
func (h *Handler) StopProgramContainer(c *gin.Context) {
	userID, courseID, ok := h.authorizeSandboxRequest(c)
	if !ok {
		return
	}

	err := service.StopProgramContainer(c.Request.Context(), h.DockerClient, h.DB, userID, courseID)
	if errors.Is(err, service.ErrSandboxNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "サンドボックスが見つかりません"})
		return
	}
	if errors.Is(err, service.ErrSandboxPublished) {
		// retryable:false - IDE内で先に公開を停止しない限り、そのまま
		// 再試行しても同じ理由で必ず失敗するため(DeleteProgramContainerと
		// 同じ方針)。
		c.JSON(http.StatusConflict, gin.H{"error": "公開中のコンテナは停止できません。先にIDE画面で公開を停止してください。", "retryable": false})
		return
	}
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス停止", "コンテナの停止に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "コンテナを停止しました"})
}

// DeleteProgramContainer は認証ユーザーの学習用コンテナを削除する(起動中でも強制削除する)。
func (h *Handler) DeleteProgramContainer(c *gin.Context) {
	userID, courseID, ok := h.authorizeSandboxRequest(c)
	if !ok {
		return
	}

	err := service.DeleteProgramContainer(c.Request.Context(), h.DockerClient, h.DB, userID, courseID)
	if errors.Is(err, service.ErrSandboxNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "サンドボックスが見つかりません"})
		return
	}
	if errors.Is(err, service.ErrSandboxPublished) {
		// retryable:false - IDE内で先に公開を停止しない限り、そのまま
		// 再試行しても同じ理由で必ず失敗するため(CreateContainerModal/
		// ErrSandboxAlreadyExistsと同じ方針)。
		c.JSON(http.StatusConflict, gin.H{"error": "公開中のコンテナは削除できません。先にIDE画面で公開を停止してください。", "retryable": false})
		return
	}
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "サンドボックス削除", "コンテナの削除に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "コンテナを削除しました"})
}
