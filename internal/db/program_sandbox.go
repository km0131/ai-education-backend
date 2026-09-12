package db

import (
	"errors"
	"time"

	"ai-education/backend/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FindProgramSandbox は指定ユーザー・クラスの「現在有効な」サンドボックス記録を
// 取得する(ソフトデリート済みの行は対象外)。見つからない場合は (nil, nil)。
func FindProgramSandbox(tx *gorm.DB, userID uuid.UUID, courseID uint) (*model.ProgramSandbox, error) {
	var sandbox model.ProgramSandbox
	err := tx.Where("user_id = ? AND course_id = ?", userID, courseID).First(&sandbox).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sandbox, nil
}

// ListActiveProgramSandboxes は「現在有効な」(ソフトデリートされていない)
// 全サンドボックス記録を、ユーザー・クラスを問わず全件返す。起動時の整合性
// チェック(service.ReconcileSandboxState)が、Docker側の実際のコンテナ一覧と
// 突き合わせるために使う。
func ListActiveProgramSandboxes(tx *gorm.DB) ([]model.ProgramSandbox, error) {
	var sandboxes []model.ProgramSandbox
	err := tx.Where("deleted_at IS NULL").Find(&sandboxes).Error
	return sandboxes, err
}

// CreateProgramSandbox は、新規に作成されたコンテナのDocker操作成功後、
// レコードを新規作成する(呼び出し側で「既存レコードなし」を確認済みであること)。
// poolSlot は割り当て済みのディスククォータプールスロット名("pool-01"等)。
func CreateProgramSandbox(tx *gorm.DB, userID uuid.UUID, courseID uint, containerID, containerName, name, status, poolSlot string) error {
	return tx.Create(&model.ProgramSandbox{
		UserID:        userID,
		CourseID:      courseID,
		ContainerID:   containerID,
		ContainerName: containerName,
		Name:          name,
		Status:        status,
		PoolSlot:      poolSlot,
		LastActiveAt:  time.Now(),
	}).Error
}

// ListUsedPoolSlots は、現在有効な(ソフトデリートされていない)全サンドボックスが
// 使用中のプールスロット名の集合を返す(コース横断・全ユーザー共通のプールのため
// courseIDでは絞り込まない)。allocatePoolSlot() が空きスロット探索に使う。
func ListUsedPoolSlots(tx *gorm.DB) (map[string]bool, error) {
	var slots []string
	err := tx.Model(&model.ProgramSandbox{}).
		Where("deleted_at IS NULL AND pool_slot != ''").
		Pluck("pool_slot", &slots).Error
	if err != nil {
		return nil, err
	}
	used := make(map[string]bool, len(slots))
	for _, s := range slots {
		used[s] = true
	}
	return used, nil
}

// UpdateProgramSandboxStatus は、既存コンテナに対するDocker操作(再開/停止)成功後に
// container_id/status/last_active_atを書き写す(表示名Nameは変更しない)。
// 再開(resume)の場合はもちろん、停止(stop)の場合にlast_active_atを更新しても
// 実害はない - 停止後のサンドボックスはstatus='running'を条件とするアイドル
// スイーパーの対象クエリ(ListIdleRunningSandboxes)から外れるため、
// タイムスタンプの意味は次にrunningへ戻ったとき(=再開時)から再び効いてくる。
func UpdateProgramSandboxStatus(tx *gorm.DB, userID uuid.UUID, courseID uint, containerID, status string) error {
	return tx.Model(&model.ProgramSandbox{}).
		Where("user_id = ? AND course_id = ? AND deleted_at IS NULL", userID, courseID).
		Updates(map[string]any{"container_id": containerID, "status": status, "last_active_at": time.Now()}).Error
}

// SetProgramSandboxLocked は教師ダッシュボードの安全対策(緊急停止/再開ロック、
// ProgramSandbox.Lockedのコメント参照)。locked=falseの時はLockedReasonも
// 空文字列へ戻す(解除した後に古い理由が残り続けるのを防ぐため)。
func SetProgramSandboxLocked(tx *gorm.DB, userID uuid.UUID, courseID uint, locked bool, reason string) error {
	if !locked {
		reason = ""
	}
	return tx.Model(&model.ProgramSandbox{}).
		Where("user_id = ? AND course_id = ? AND deleted_at IS NULL", userID, courseID).
		Updates(map[string]any{"locked": locked, "locked_reason": reason}).Error
}

// TouchProgramSandboxActivity は、コンテナのDocker状態(status)は変えずに、
// last_active_atだけを「今」に更新する。シェル/LSP用の短命チケット発行
// (service.IssueShellTicket、生徒が実際にサンドボックスを使い始める合図)の
// たびに呼び出し、アイドルタイムアウトの自動停止スイーパーの対象から一時的に
// 外すために使う。対象行が無い(サンドボックス未作成/削除済み)場合も
// エラーにはしない(0行更新のno-opとして扱う、呼び出し側でチケット発行自体を
// 失敗させたくないため)。
func TouchProgramSandboxActivity(tx *gorm.DB, userID uuid.UUID, courseID uint) error {
	return tx.Model(&model.ProgramSandbox{}).
		Where("user_id = ? AND course_id = ? AND deleted_at IS NULL", userID, courseID).
		Update("last_active_at", time.Now()).Error
}

// ListIdleRunningSandboxes は、稼働中(status='running')・非公開(published=false)・
// 最終活動時刻がcutoffより前の「現在有効な」サンドボックス一覧を返す。
// アイドルタイムアウトの自動停止スイーパー(service.SweepIdleSandboxes)が使う
// (NextPlan.md フェーズ2「アイドルタイムアウトによる自動停止ロジック
// (公開中のコンテナは対象外)」)。公開中の除外はpublished=falseの条件だけで
// 表現しており、フェーズ7で公開機能を実装してもこのクエリ自体は変更不要。
func ListIdleRunningSandboxes(tx *gorm.DB, cutoff time.Time) ([]model.ProgramSandbox, error) {
	var sandboxes []model.ProgramSandbox
	err := tx.
		Where("deleted_at IS NULL AND status = ? AND published = ? AND last_active_at < ?", "running", false, cutoff).
		Find(&sandboxes).Error
	return sandboxes, err
}

// ListRunningProgramSandboxes は、現在稼働中(status='running')の「現在有効な」
// サンドボックス一覧を、コース/ユーザーを問わずシステム全体で返す。リソース
// 使用状況ダッシュボード(service.CollectSystemMetrics)が、Docker側から
// 個々のCPU/メモリ統計を取得する対象コンテナを決めるために使う。
func ListRunningProgramSandboxes(tx *gorm.DB) ([]model.ProgramSandbox, error) {
	var sandboxes []model.ProgramSandbox
	err := tx.Where("deleted_at IS NULL AND status = ?", "running").Find(&sandboxes).Error
	return sandboxes, err
}

// DeleteProgramSandbox は指定ユーザー・クラスのサンドボックス記録をソフトデリートする
// (以前の割り当て履歴を残す。GORMのDeletedAtによる標準的なソフトデリート)。
func DeleteProgramSandbox(tx *gorm.DB, userID uuid.UUID, courseID uint) error {
	return tx.Where("user_id = ? AND course_id = ?", userID, courseID).Delete(&model.ProgramSandbox{}).Error
}

// CountPublishedSandboxes は現在公開中(published=true)のサンドボックス総数を
// 返す(コース/ユーザーを問わずシステム全体)。PublishSandboxが
// PUBLISH_MAX_CONCURRENT(publish_service.go)との比較に使う。
func CountPublishedSandboxes(tx *gorm.DB) (int64, error) {
	var count int64
	err := tx.Model(&model.ProgramSandbox{}).
		Where("deleted_at IS NULL AND published = ?", true).
		Count(&count).Error
	return count, err
}

// ExistsPublishSlug は指定slugが(現在有効な行の中に)既に使われているかを返す。
// generatePublishSlug(publish_service.go)の衝突チェックに使う - 過去に公開
// 停止/期限切れになった行のslugも含めて重複を避ける(履歴として残すため
// 再利用しない方針)。
func ExistsPublishSlug(tx *gorm.DB, slug string) (bool, error) {
	var count int64
	err := tx.Model(&model.ProgramSandbox{}).
		Where("publish_slug = ?", slug).
		Count(&count).Error
	return count > 0, err
}

// PublishProgramSandbox は指定サンドボックスを公開状態にする(publish_service.go
// のPublishSandboxが同時公開数上限チェック後に呼ぶ)。
func PublishProgramSandbox(tx *gorm.DB, userID uuid.UUID, courseID uint, slug string, port int, expiresAt time.Time) error {
	return tx.Model(&model.ProgramSandbox{}).
		Where("user_id = ? AND course_id = ? AND deleted_at IS NULL", userID, courseID).
		Updates(map[string]any{
			"published":          true,
			"publish_slug":       slug,
			"publish_port":       port,
			"publish_expires_at": expiresAt,
		}).Error
}

// UnpublishProgramSandbox は指定サンドボックスの公開を停止する(生徒本人の
// 明示操作、または自動失効スケジューラの両方から呼ばれる)。PublishSlug/
// PublishPortは履歴としてそのまま残す(公開判定は必ずpublished=trueと
// 併せて行うため、これらの値が残っていてもルーティングには使われない)。
func UnpublishProgramSandbox(tx *gorm.DB, userID uuid.UUID, courseID uint) error {
	return tx.Model(&model.ProgramSandbox{}).
		Where("user_id = ? AND course_id = ? AND deleted_at IS NULL", userID, courseID).
		Update("published", false).Error
}

// UnpublishAllPublishedInCourse は指定クラスで現在公開中の全サンドボックスを
// 一括で非公開にする(教師ダッシュボードの「公開を一括停止」、
// TeacherDashboardModal.tsx)。1本のUPDATE文で完結するため、公開自体は
// DBのフラグだけで表現される(Docker操作を伴わない、UnpublishProgramSandbox
// と同じ理由)。戻り値は実際に非公開化した件数。
func UnpublishAllPublishedInCourse(tx *gorm.DB, courseID uint) (int64, error) {
	result := tx.Model(&model.ProgramSandbox{}).
		Where("course_id = ? AND deleted_at IS NULL AND published = ?", courseID, true).
		Update("published", false)
	return result.RowsAffected, result.Error
}

// ExtendProgramSandboxPublish は公開期限を延長する(公開中でなければ0行更新、
// 呼び出し元のExtendPublishがpublished=trueを事前確認する)。
func ExtendProgramSandboxPublish(tx *gorm.DB, userID uuid.UUID, courseID uint, newExpiresAt time.Time) error {
	return tx.Model(&model.ProgramSandbox{}).
		Where("user_id = ? AND course_id = ? AND deleted_at IS NULL AND published = ?", userID, courseID, true).
		Update("publish_expires_at", newExpiresAt).Error
}

// FindSandboxByPublishSlug は公開中(published=true)のサンドボックスをslugから
// 引く - 公開プレビューのリバースプロキシ(PublicPreviewProxy、
// public_preview_handler.go)がリクエストパス先頭のslugから対象コンテナを
// 解決するために使う。見つからない場合は(nil, nil) - 呼び出し元は404として
// 扱う(存在しないslug/既に公開停止されたslugのどちらもこの経路では区別しない
// - 攻撃者に「存在するslugかどうか」の情報を与えないため)。
func FindSandboxByPublishSlug(tx *gorm.DB, slug string) (*model.ProgramSandbox, error) {
	var sandbox model.ProgramSandbox
	err := tx.Where("publish_slug = ? AND published = ? AND deleted_at IS NULL", slug, true).First(&sandbox).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sandbox, nil
}

// ListExpiredPublishedSandboxes は公開期限(publish_expires_at)を過ぎてもなお
// published=trueのままの「現在有効な」サンドボックス一覧を返す。自動失効
// スケジューラ(publish_service.goのSweepExpiredPublishesOnce)が使う。
func ListExpiredPublishedSandboxes(tx *gorm.DB, cutoff time.Time) ([]model.ProgramSandbox, error) {
	var sandboxes []model.ProgramSandbox
	err := tx.
		Where("deleted_at IS NULL AND published = ? AND publish_expires_at < ?", true, cutoff).
		Find(&sandboxes).Error
	return sandboxes, err
}

// ListProgramSandboxesForCourse は指定クラスの「現在有効な」サンドボックス一覧を返す
// (画像分類AIの「みんなが作ったAIモデル」一覧と同様、クラスメンバー全員に見える想定)。
// ステータスはDB側の記録(直近のstart/stop成功時に書き写したもの)をそのまま返す。
// Dockerへの都度の問い合わせはしない(一覧表示のたびにN回Docker APIを叩くのを避けるため)。
func ListProgramSandboxesForCourse(tx *gorm.DB, courseID uint) ([]model.ProgramContainerCard, error) {
	var cards []model.ProgramContainerCard
	err := tx.Model(&model.ProgramSandbox{}).
		Select(`
			program_sandboxes.name AS name,
			program_sandboxes.container_name AS container_name,
			SPLIT_PART(users.name, '-', 1) AS student_name,
			program_sandboxes.status AS status,
			program_sandboxes.published AS published,
			program_sandboxes.publish_slug AS publish_slug,
			program_sandboxes.publish_expires_at AS publish_expires_at,
			program_sandboxes.last_active_at AS last_active_at,
			program_sandboxes.locked AS locked,
			program_sandboxes.locked_reason AS locked_reason,
			program_sandboxes.user_id AS user_id
		`).
		Joins("JOIN users ON users.id = program_sandboxes.user_id").
		Where("program_sandboxes.course_id = ? AND program_sandboxes.deleted_at IS NULL", courseID).
		Order("program_sandboxes.created_at ASC").
		Scan(&cards).Error
	return cards, err
}
