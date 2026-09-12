package service

import (
	"context"
	"errors"
	"log"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/docker"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrNotCourseTeacher is returned by the teacher-dashboard operations below
// when the caller isn't the teacher of the given course - the PASETOクレーム
// 自体(utils.GetUserTeacher)は「この人は(どこかの)先生である」としか
// 分からないため、実際の権限確認は必ずこのコースのteacher_idとの一致で
// 行う(db.IsCourseTeacher、db.SetAiCreationBlockedと同じ方針)。
var ErrNotCourseTeacher = errors.New("このクラスの担当教師のみ実行できます")

// UnpublishSandboxAsTeacher lets a course's teacher force-unpublish a
// specific student's sandbox(教師ダッシュボードの個別「公開停止」、
// TeacherDashboardModal.tsx) - 生徒本人のUnpublishSandbox
// (publish_service.go)と処理自体は同じだが、呼び出し元が生徒本人ではなく
// 担当教師であることを確認する点だけが異なる。
func UnpublishSandboxAsTeacher(tx *gorm.DB, teacherID, targetUserID uuid.UUID, courseID uint) error {
	isTeacher, err := db.IsCourseTeacher(tx, courseID, teacherID)
	if err != nil {
		return err
	}
	if !isTeacher {
		return ErrNotCourseTeacher
	}
	return UnpublishSandbox(tx, targetUserID, courseID)
}

// UnpublishAllForCourse unpublishes every currently-published sandbox in
// courseID at once(教師ダッシュボードの「公開を一括停止」)。DBのフラグ
// 更新のみでDockerは一切操作しない(UnpublishSandboxと同じ理由)。
func UnpublishAllForCourse(tx *gorm.DB, teacherID uuid.UUID, courseID uint) (count int64, err error) {
	isTeacher, err := db.IsCourseTeacher(tx, courseID, teacherID)
	if err != nil {
		return 0, err
	}
	if !isTeacher {
		return 0, ErrNotCourseTeacher
	}
	return db.UnpublishAllPublishedInCourse(tx, courseID)
}

// StopAllContainersResult summarizes a bulk-stop pass(教師ダッシュボードの
// 「コンテナ一括停止」)。
type StopAllContainersResult struct {
	Stopped          int `json:"stopped"`
	SkippedPublished int `json:"skipped_published"`
	Failed           int `json:"failed"`
}

// StopAllContainersForCourse stops every currently running, non-published
// sandbox in courseID(教師ダッシュボードの「コンテナ一括停止」)。公開中の
// ものは(単体停止と同じ理由 - ErrSandboxPublished参照)スキップし、件数として
// 報告するに留める。「先に公開を一括停止してから、改めて一括停止する」という
// 2段階の操作を強制する形になる - 停止操作自体が公開中コンテナを暗黙に
// 巻き込んでしまわないようにするため。
func StopAllContainersForCourse(ctx context.Context, cli docker.ContainerAPI, database *gorm.DB, teacherID uuid.UUID, courseID uint) (StopAllContainersResult, error) {
	var result StopAllContainersResult

	isTeacher, err := db.IsCourseTeacher(database, courseID, teacherID)
	if err != nil {
		return result, err
	}
	if !isTeacher {
		return result, ErrNotCourseTeacher
	}

	cards, err := db.ListProgramSandboxesForCourse(database, courseID)
	if err != nil {
		return result, err
	}

	for _, card := range cards {
		if card.Status != "running" {
			continue
		}
		if card.Published {
			result.SkippedPublished++
			continue
		}
		stopErr := database.Transaction(func(tx *gorm.DB) error {
			return StopProgramContainer(ctx, cli, tx, card.UserID, courseID)
		})
		if stopErr != nil {
			result.Failed++
			log.Printf("[TEACHER-DASHBOARD] 一括停止に失敗しました course=%d user=%s err=%v", courseID, card.UserID, stopErr)
			continue
		}
		result.Stopped++
	}

	return result, nil
}

// EmergencyStopContainer forcibly kills(docker kill、SIGKILL相当 -
// ContainerStopのSIGTERM+猶予時間とは違い、シャットダウンの猶予すら
// 与えない)the target student's running container(教師ダッシュボードの
// 「緊急停止」ボタン、安全対策) - 生徒がコンテナ内で悪質な処理を実行して
// いる場合に即座に止めるためのもの。既に停止済みならDocker操作は行わず、
// DB上のstatusだけ合わせる(冪等)。
func EmergencyStopContainer(ctx context.Context, cli docker.ContainerAPI, tx *gorm.DB, teacherID, targetUserID uuid.UUID, courseID uint) error {
	isTeacher, err := db.IsCourseTeacher(tx, courseID, teacherID)
	if err != nil {
		return err
	}
	if !isTeacher {
		return ErrNotCourseTeacher
	}

	existing, err := findSandbox(ctx, cli, targetUserID, courseID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrSandboxNotFound
	}
	if existing.State != "running" {
		return db.UpdateProgramSandboxStatus(tx, targetUserID, courseID, existing.ID, "stopped")
	}
	if err := cli.ContainerKill(ctx, existing.ID, "SIGKILL"); err != nil {
		return err
	}
	return db.UpdateProgramSandboxStatus(tx, targetUserID, courseID, existing.ID, "stopped")
}

// ResumeContainerAsTeacher lets a course's teacher resume a specific
// student's stopped sandbox(教師ダッシュボードの「▶ 再開」ボタン) - あえて
// ロック確認(rejectIfLocked)を経由しない。ロックは生徒本人が勝手に再開
// できないようにするためのものであって、担当教師自身の操作まで縛る意図では
// ない(事情確認のために教師が一時的に起動し、生徒には触らせない、という
// 運用を可能にするため)。ロック状態自体はこの操作では変更しない - 生徒に
// よる再開はこの後も引き続きErrSandboxLockedで弾かれる。
func ResumeContainerAsTeacher(ctx context.Context, cli docker.ContainerAPI, tx *gorm.DB, teacherID, targetUserID uuid.UUID, courseID uint) (containerID string, alreadyRunning bool, err error) {
	isTeacher, err := db.IsCourseTeacher(tx, courseID, teacherID)
	if err != nil {
		return "", false, err
	}
	if !isTeacher {
		return "", false, ErrNotCourseTeacher
	}
	return resumeProgramContainer(ctx, cli, tx, targetUserID, courseID)
}

// SetSandboxLocked locks/unlocks the target student's sandbox against being
// resumed(教師ダッシュボードの「ロック」ボタン、安全対策) -
// ErrSandboxLocked/ProgramSandbox.Lockedのコメント参照。Dockerは一切
// 操作しない(DBのフラグだけを変える、実際の強制停止はEmergencyStopContainer
// の役目)。
func SetSandboxLocked(tx *gorm.DB, teacherID, targetUserID uuid.UUID, courseID uint, locked bool, reason string) error {
	isTeacher, err := db.IsCourseTeacher(tx, courseID, teacherID)
	if err != nil {
		return err
	}
	if !isTeacher {
		return ErrNotCourseTeacher
	}
	return db.SetProgramSandboxLocked(tx, targetUserID, courseID, locked, reason)
}
