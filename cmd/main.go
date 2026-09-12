package main // ← 必ず1行目！

import (
	"ai-education/backend/internal/worker"
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "ai-education/backend/docs" // 1. swag initで生成されるdocsをインポート

	"ai-education/backend/internal/controller"
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/docker"
	"ai-education/backend/internal/handler"
	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// @Summary      疎通確認
// @Description  サーバーの生存確認用
// @Tags         system
// @Accept       json
// @Produce      json
// @Success      200 {object} map[string]string
// @Router       /ping [get]
func PingHandler(c *gin.Context) {
	c.JSON(200, gin.H{"message": "Hello from Go Backend!"})
}

// @title           AI Education API
// @version         1.0
// @description     AI EducationのバックエンドAPIサーバーです。
// @host            localhost:8080
// @BasePath        /
func main() {

	db.InitDB()
	// マイグレーション失敗を握りつぶさない: 以前はエラーを無視して起動を続行して
	// いたため、スキーマが古いまま(例: 新しいNOT NULL列が実際には作られていない)
	// 動き続け、その列に触れる機能だけが謎の500を返す事態になっていた。
	if err := db.Migrate(); err != nil {
		log.Fatalf("マイグレーションに失敗したため起動を中止します: %v", err)
	}

	worker.StartGPUWorker(db.DB)

	r := gin.Default()

	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// Dockerクライアントの初期化(DOCKER_HOST経由でunix socket/tcp/sshいずれの
	// 接続先にも対応。接続自体は遅延されるため、ここで失敗するのは設定不備のみ)
	dockerClient, err := docker.NewClient()
	if err != nil {
		log.Fatalf("Dockerクライアントの初期化に失敗しました: %v", err)
	}
	// リクエストを受け付け始める前に、DB上のサンドボックス記録をDocker側の
	// 実際の状態に合わせ込む(前回プロセスの異常終了・host/dockerd再起動等で
	// 生じたズレを起動のたびに自己修復する)。DOCKER_HOSTがssh://の場合に接続先
	// が一時的に不通だと無期限にハングしうるため、タイムアウトで打ち切る
	// (打ち切られても致命的ではない - service.ReconcileSandboxState参照)。
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), service.ReconcileTimeout)
	service.ReconcileSandboxState(reconcileCtx, dockerClient, db.DB)
	reconcileCancel()
	service.StartIdleSandboxSweeper(dockerClient, db.DB)
	// 単一サブドメイン(preview.a-kiis.com)パスベース公開機能(NextPlan.md
	// フェーズ7)の自動失効スケジューラ。
	service.StartPublishExpirySweeper(db.DB)

	// ハンドラーの初期化
	h := handler.Handler{
		DB:           db.DB,
		DockerClient: dockerClient,
	}

	r.GET("/images/certification/*filename", h.PostPasswordImage)
	r.GET("/images/ai_photogrph/*filename", h.GetAiPhotographImage)
	r.GET("/images/test_photogrph/*filename", h.GetTestImage)
	r.GET("/storage/models/*filename", h.GetModelFile)
	r.POST("/api/callback/model_ready", utils.MachineToMachineAuth(), controller.HandleModelReady)
	r.POST("/api/callback/test_result", utils.MachineToMachineAuth(), controller.HandleTestReady)

	v0 := r.Group("/api/v0")
	{
		// ルーティング
		v0.POST("/", h.PostLogin)
		v0.GET("/signup", h.GetSignup)
		v0.POST("/signup", h.PostSignup)
		v0.POST("/login_registrer", h.PostLoginRegistrer)
		v0.POST("/login_qr", h.PostLoginQR)
	}
	// 画像分類AI
	v1 := r.Group("/api/v1")
	{
		// main関数の中のインライン定義ではなく、上で定義した関数を使う
		v1.GET("/ping", PingHandler)
		authGroup := v1.Group("/")
		authGroup.Use(utils.AuthMiddleware(h.DB))
		{
			authGroup.POST("/create_class", h.CreateClass)
			authGroup.POST("/join_class", h.JoinClass)
			authGroup.GET("/user", h.User)
			authGroup.GET("/my_courses", h.MyCourses)
			authGroup.GET("/courses/:id", h.RemoveClass)
			aiGroup := authGroup.Group("/ai")
			aiGroup.Use(utils.AuthMiddleware(h.DB))
			{
				aiGroup.POST("/upload_image", h.UploadImage)
				aiGroup.POST("/aicard", h.AiCard)
				aiGroup.POST("/ai_creation", h.AiCreation)
				aiGroup.POST("/get_description", h.GetDescription)
				aiGroup.PUT("/create_description", h.CreateDescription)
				aiGroup.POST("/image_acquisition", h.ImageAcquisition)
				aiGroup.POST("/image_updated", h.ImageUpdated)
				aiGroup.POST("/delete_image", h.DeleteImage)
				aiGroup.POST("/up_label", h.UpLabel)
				aiGroup.POST("/ai_model", h.AiModel)
				aiGroup.POST("/photo_status", h.PhotoStatus)
				aiGroup.POST("/block_status", h.AiCreationBlockStatus)
				aiGroup.POST("/block_toggle", h.SetAiCreationBlockStatus)
			}
			testGroup := authGroup.Group("/test")
			testGroup.Use(utils.AuthMiddleware(h.DB))
			{
				testGroup.POST("/uploading_test_image", h.UploadingTestImage)
				testGroup.POST("/get_images", h.GetImage)
				testGroup.POST("/delete_tsst_image", h.DeleteTestImage)
				testGroup.POST("/get_test_label", h.GetTestLabel)
				testGroup.POST("/get_test_label_options", h.GetTestLabelOptions)
				testGroup.POST("/up_test_label", h.UpTestLabel)
				testGroup.POST("/get_test_label_map", h.GetTestLabelMap)
				testGroup.POST("/up_test_label_map", h.UpStudentTestLabel)
				testGroup.POST("/execution", h.TestExecution)
				testGroup.POST("/photo_status", h.TestPhotoStatus)
			}
			resultGroup := authGroup.Group("/result")
			resultGroup.Use(utils.AuthMiddleware(h.DB))
			{
				resultGroup.POST("/training_curve", h.TrainingCurve)
				resultGroup.POST("/test_results", h.TestResults)
				resultGroup.POST("/test_results_imge", h.TestResultsImge)
				resultGroup.POST("/image_evaluation_get", h.ImageEvaluationGet)

			}
		}
	}
	// 中高生向けプログラム作成機能
	v2 := r.Group("/api/v2")
	{
		v2.GET("/ping", PingHandler)
		// シェル用WebSocket: PASETOのAuthMiddlewareではなく、
		// IssueShellTicket()で発行された短命ワンタイムチケット(?ticket=)で
		// 認証する(NextPlan.md §3.2)。ブラウザのWebSocket APIはAuthorization
		// ヘッダーを付けられないため、この経路だけ意図的に認証グループの外に
		// 置いている。
		v2.GET("/program/container/shell", h.ShellWebSocket)
		// 言語サーバー用WebSocket(汎用LSP中継、NextPlan.md フェーズ6): シェルと
		// 同じ理由でAuthMiddlewareの外に置き、同じ短命ワンタイムチケットで
		// 認証する(IssueShellTicketはシェル/LSPどちらの接続にも使い回せる
		// 汎用チケットで、種別を区別しない)。
		v2.GET("/program/container/lsp", h.LspWebSocket)
		// IDE内蔵Webプレビュー(preview_handler.go): <iframe src=...>もAuthorization
		// ヘッダーを付けられないため、同じ理由でAuthMiddlewareの外に置く。ただし
		// シェル/LSPの使い切りチケットとは違う専用のチケット(IssuePreviewTicket、
		// preview_ticket_service.go)で認証する - 1ページの表示でHTML本体・CSS・
		// JS・画像など何本ものリクエストが飛ぶため、有効期限内は使い回せる必要が
		// ある。
		v2.Any("/program/container/preview/:ticket/:port/*path", h.PreviewProxy)
		// 「実行」ボタンの.html拡張(静的ファイル配信、preview_handler.go):
		// サーバープロセスを起動せず、ワークスペース内のファイルを直接読んで
		// 返すだけなのでGETのみ。認証は上と同じプレビュー用チケット。
		v2.GET("/program/container/preview-file/:ticket/*path", h.PreviewFile)
		// 講師による生徒セッションのリアルタイム監視・共同操作(ライブセッション
		// 同期): シェル/LSPと同じ理由でAuthMiddlewareの外に置き、専用の短命
		// ワンタイムチケット(IssueOwnLiveSessionTicket/IssueTeacherLiveSessionTicket)
		// で認証する(live_session_handler.go)。
		v2.GET("/program/container/live/shell", h.LiveShellWebSocket)
		v2.GET("/program/container/live/editor", h.LiveEditorWebSocket)
		// リソース使用状況ダッシュボード(教師専用、TeacherDashboardModal.tsx):
		// 同じ理由でAuthMiddlewareの外に置き、専用の短命ワンタイムチケット
		// (IssueMetricsTicket)で認証する(metrics_handler.go)。
		v2.GET("/admin/metrics/ws", h.MetricsWebSocket)

		authGroup := v2.Group("/")
		authGroup.Use(utils.AuthMiddleware(h.DB))
		{
			authGroup.GET("/feature-types", h.ListFeatureTypes)
			// リソース使用状況ダッシュボード(教師専用)の講師向けチケット発行。
			// コース/生徒を問わないシステム全体の計測のため、/program配下では
			// なく独立した/admin配下に置く(ハンドラー内部で「先生であること」を
			// 確認する、metrics_handler.go参照)。
			authGroup.POST("/admin/metrics/ticket", h.IssueMetricsTicket)
			// 単一サブドメイン(preview.a-kiis.com)パスベース公開機能
			// (NextPlan.md フェーズ7)の通報受け口。ログイン済みユーザーのみ
			// (匿名通報はスパム/荒らしの温床になりやすいため)。
			authGroup.POST("/report", h.ReportPublishedContent)

			prGroup := authGroup.Group("/program")
			prGroup.Use(utils.AuthMiddleware(h.DB))
			{
				prGroup.POST("/containers", h.ListProgramContainers)
				prGroup.POST("/container/start", h.StartProgramContainer)
				prGroup.POST("/container/resume", h.ResumeProgramContainer)
				prGroup.POST("/container/stop", h.StopProgramContainer)
				prGroup.POST("/container/ticket", h.IssueShellTicket)
				prGroup.POST("/container/preview-ticket", h.IssuePreviewTicket)
				prGroup.POST("/container/commit", h.CommitWorkspace)
				prGroup.GET("/container/files", h.ListSandboxFiles)
				prGroup.GET("/container/file", h.GetSandboxFileContent)
				prGroup.POST("/container/file", h.CreateSandboxPath)
				prGroup.PUT("/container/file", h.MoveSandboxPath)
				prGroup.PATCH("/container/file", h.WriteSandboxFileContent)
				prGroup.DELETE("/container/file", h.DeleteSandboxPath)
				prGroup.DELETE("/container", h.DeleteProgramContainer)
				// 単一サブドメイン(preview.a-kiis.com)パスベース公開機能
				// (NextPlan.md フェーズ7)の生徒本人向け管理API。
				prGroup.POST("/container/publish", h.PublishSandboxContainer)
				prGroup.POST("/container/unpublish", h.UnpublishSandboxContainer)
				prGroup.POST("/container/publish/extend", h.ExtendSandboxPublish)
				prGroup.GET("/container/publish-status", h.GetSandboxPublishStatus)
				// 講師による生徒セッションのリアルタイム監視・共同操作
				// (ライブセッション同期)の生徒本人向けチケット発行。
				prGroup.POST("/container/live-session-ticket", h.IssueOwnLiveSessionTicket)
				// 生徒間でのリアルタイム相互閲覧(Peer Viewer、読み取り専用)
				// 機能の閲覧者向けチケット発行 - 担当教師である必要は無く、
				// 同じクラスの生徒同士であれば誰でも使える。
				prGroup.POST("/peer/live-session-ticket", h.IssuePeerLiveSessionTicket)

				// 教師ダッシュボード(TeacherDashboardModal.tsx)。生徒本人向け
				// APIとは別グループにせず同じ/program配下に置くが、各ハンドラー
				// 内部で「先生であること」+「このクラスの担当教師であること」
				// を確認する(teacher_dashboard_handler.go)。
				teacherGroup := prGroup.Group("/teacher")
				teacherGroup.Use(utils.AuthMiddleware(h.DB))
				{
					teacherGroup.POST("/unpublish", h.TeacherUnpublishSandbox)
					teacherGroup.POST("/unpublish-all", h.TeacherUnpublishAll)
					teacherGroup.POST("/stop-all", h.TeacherStopAll)
					// 講師による生徒セッションのリアルタイム監視・共同操作
					// (ライブセッション同期)の講師向けチケット発行。
					teacherGroup.POST("/live-session-ticket", h.IssueTeacherLiveSessionTicket)
					// 安全対策: 緊急停止(docker kill)/再開ロック。
					teacherGroup.POST("/emergency-stop", h.TeacherEmergencyStop)
					teacherGroup.POST("/lock", h.TeacherSetSandboxLock)
					// 教師ダッシュボードからの再開(ロック中でも実行可能、
					// ResumeContainerAsTeacherのコメント参照)。
					teacherGroup.POST("/resume", h.TeacherResumeSandbox)
					// 講師サポート画面のターミナル/LSP接続向けチケット発行
					// (IssueTeacherShellTicketのコメント参照)。
					teacherGroup.POST("/shell-ticket", h.IssueTeacherShellTicket)
				}
			}

			sandboxGroup := authGroup.Group("/sandbox")
			sandboxGroup.Use(utils.AuthMiddleware(h.DB))
			{
				sandboxGroup.GET("/git/status", h.GitStatus)
				sandboxGroup.GET("/git/history", h.GitHistory)
				sandboxGroup.GET("/git/diff/:hash", h.GitDiff)
				sandboxGroup.POST("/git/revert", h.RevertWorkspace)
			}
		}
	}

	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// 単一サブドメイン(preview.a-kiis.com)パスベース公開機能(NextPlan.md
	// フェーズ7)。本番APIのGinエンジン(r、Hostは通常ai-back.a-kiis.com等)
	// とは完全に別のGinエンジンにする - 認証(PASETO)の外側にあるべき経路
	// であり、r側のミドルウェア/ルート空間と混ざらないようにするため。
	// どちらへ振り分けるかはHostヘッダーで判定する(hostRouter、下記)。
	publicPreviewHost := publishPreviewHost()
	previewRouter := gin.New()
	previewRouter.Use(gin.Recovery())
	// リバースプロキシでのレート制限(NextPlan.md フェーズ7「悪用対策」) -
	// 認証が無く誰でも叩けるエンドポイントのため、同一IPからの過剰リクエストを
	// スロットリングする。
	previewRouter.Use(utils.PerIPRateLimiter(publishRateLimitPerMinute, time.Minute))
	previewRouter.Any("/:slug/*path", h.PublicPreviewProxy)

	log.Printf("Server listening on :8080 (公開プレビューはHost: %sで振り分け)", publicPreviewHost)

	server := &http.Server{
		Addr:    ":8080",
		Handler: newHostRouter(publicPreviewHost, previewRouter, r),
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("サーバーの起動に失敗しました: %v", err)
	}
}

// publishRateLimitPerMinute: 公開プレビューへの同一IPからのアクセスを
// 1分間にこの回数まで許可する(タスク指示書の例「1分間に100リクエスト超」)。
// ページ1回の表示でHTML本体・CSS・JS・画像など何本ものリクエストが飛ぶ
// ことを踏まえた値 - 運用でチューニングする対象ではないため.envには出さない
// (他の同時実行数上限等と違い、悪用対策の閾値そのものを生徒/教員が触れる
// 必要は無い)。
const publishRateLimitPerMinute = 100

// publishPreviewHost reads PUBLISH_PREVIEW_HOST from the environment
// (Hostヘッダーの一致判定に使う実際のホスト名、例: "preview.a-kiis.com")、
// falling back to a name based on PUBLISH_BASE_URL(service.PublishBaseURL)
// so a working default exists even before this env var is configured for a
// given deployment.
func publishPreviewHost() string {
	if v := os.Getenv("PUBLISH_PREVIEW_HOST"); v != "" {
		return v
	}
	base := strings.TrimPrefix(strings.TrimPrefix(service.PublishBaseURL(), "https://"), "http://")
	return strings.TrimSuffix(base, "/")
}

// hostRouter dispatches incoming requests to one of two http.Handlers based
// on the Host header alone - previewHandler for previewHost(ポート番号が
// 付いていても無視して比較する)、それ以外はすべてdefaultHandler(通常の
// 認証済みAPI群)。単一のGinエンジンでは「同じパスをHostによって別処理に
// 振り分ける」ことがルーティングツリーの都合上素直にできない
// (:slug/*pathのようなワイルドカードルートを、本来のAPI群のルートと
// 同じツリーに混在させたくない)ため、http.Handlerレベルの薄いラッパーで
// Host単位に完全分離している。
type hostRouter struct {
	previewHost    string
	previewHandler http.Handler
	defaultHandler http.Handler
}

func newHostRouter(previewHost string, previewHandler, defaultHandler http.Handler) *hostRouter {
	return &hostRouter{previewHost: previewHost, previewHandler: previewHandler, defaultHandler: defaultHandler}
}

func (h *hostRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if idx := strings.IndexByte(host, ':'); idx != -1 {
		host = host[:idx]
	}
	if host == h.previewHost {
		h.previewHandler.ServeHTTP(w, r)
		return
	}
	h.defaultHandler.ServeHTTP(w, r)
}
