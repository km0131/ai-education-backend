package db

import (
	"ai-education/backend/internal/model"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"encoding/json"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// var Tx *gorm.DB

// GetOrCreateConfig: プロジェクトの「箱」を取得または自動作成
func GetOrCreateConfig(tx *gorm.DB, userID uuid.UUID, courseID uint, title string, us uuid.UUID) (*model.AiConfiguration, error) {
	config := &model.AiConfiguration{
		ProjectUUID: us,
		StudentID:   userID,
		CourseID:    courseID,
		Title:       title,
		IsShared:    true,
	}

	// 悲観的ロックの代わりに「OnConflict(何もしない)」でインサートを試みる
	// これにより、複数リクエストが同時に来ても、DBレベルで「1つだけINSERT、残りは何もしない」に一列化される
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_uuid"}},
		DoNothing: true,
	}).Create(config)

	if result.Error != nil {
		return nil, result.Error
	}

	// 自分のリクエストによって新規作成されたか（RowsAffected > 0）を判定
	if result.RowsAffected > 0 {
		// 【新規作成（1枚目のリクエスト）の場合のみルール2を実行】
		// 同じ student_id & course_id で、今作ったばかりの自分のレコード(us)以外の過去作を論理削除
		err := tx.Model(&model.AiConfiguration{}).
			Where("student_id = ? AND course_id = ? AND project_uuid != ?", userID, courseID, us.String()).
			Delete(&model.AiConfiguration{}).Error
		if err != nil {
			return nil, err
		}

		return config, nil
	}

	// RowsAffected == 0（2枚目以降のリクエスト）の場合
	// すでに1枚目がINSERTを済ませている（あるいは別トランザクションがインサート中）ので、
	// 安全に最新のレコード情報をSELECTして取得・返却する
	err := tx.Where("project_uuid = ?", us).First(config).Error
	if err != nil {
		return nil, err
	}

	return config, nil
}

// CreateCategoryWithHistory: ラベル情報の履歴管理付き作成
func CreateCategoryWithHistory(tx *gorm.DB, configID uuid.UUID, index int, title string) (*model.AiCategory, error) {
	// UUIDの生成
	newID := uuid.New()
	// 新規作成
	category := &model.AiCategory{
		ConfigID:      configID,
		CategoryID:    newID,
		CategoryIndex: index,
		Title:         title,
	}
	err := tx.Create(category).Error
	return category, err
}

// CreatePhotograph: 学習データの保存
func CreatePhotograph(tx *gorm.DB, categoryID uuid.UUID, userID uuid.UUID, path string) (*model.AiPhotograph, error) {
	// 新規作成
	photo := &model.AiPhotograph{
		CategoryID:     categoryID,
		StudentID:      userID,
		PhotographPath: path,
		IsAnalyzed:     false,
	}
	err := tx.Create(photo).Error
	return photo, err
}

// 画像DBの検索
func GetPhotographByID(tx *gorm.DB, id string) (*model.AiPhotograph, error) {
	var photo model.AiPhotograph
	result := tx.Where("id = ?", id).First(&photo)

	return &photo, result.Error
}

// 分析結果を保存
func UpdatePhotoAnalysis(tx *gorm.DB, photoID int, data model.AnalysisData) error {
	if tx == nil {
		return fmt.Errorf("database connection is nil")
	}

	// 🌟 一度 JSON バイト列にシリアライズする
	bytes, err := json.Marshal(data.DiversityVector)
	if err != nil {
		return fmt.Errorf("failed to marshal diversity vector: %v", err)
	}

	// 🌟 json.RawMessage にキャスト（これで GORM はタプル化せずそのまま jsonb に流します）
	diversityJSONRaw := json.RawMessage(bytes)

	err = tx.Model(&model.AiPhotograph{}).Where("id = ?", photoID).Updates(map[string]interface{}{
		"updated_at":       time.Now(),
		"is_analyzed":      true,
		"saturation":       data.Saturation,
		"brightness":       data.Brightness,
		"sharpness":        data.Sharpness,
		"diversity_vector": diversityJSONRaw, // 🌟 キャストしたカスタム型を渡す
	}).Error

	return err
}

// CreateTrainingJobWithSnapshot: 指定されたプロジェクトUUID(configID)の現在の全画像をスナップショットとして固定し、Jobを作成する
func CreateTrainingJobWithSnapshot(database *gorm.DB, configID uuid.UUID) (*model.AiTrainingJob, error) {
	if database == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	// トランザクション開始
	tx := database.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 1. 最初から INSERT (OnConflict: DoNothing) を試みる
	var job model.AiTrainingJob
	err := tx.Where(
		"config_id = ? AND status = ?",
		configID,
		"pending",
	).First(&job).Error

	if err != nil {
		tx.Rollback()

		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("pending training job not found")
		}

		return nil, err
	}

	fmt.Printf("[Debug] Job(ID:%d)へスナップショットを追加します。\n", job.ID)

	// 3. スナップショット対象のデータ（現在のプロジェクトに属するすべての写真と、そのカテゴリのインデックス番号）を取得
	type CurrentPhotoData struct {
		PhotoID       uint
		CategoryIndex int
	}
	var currentPhotos []CurrentPhotoData

	err = tx.Model(&model.AiPhotograph{}).
		Select("ai_photographs.id as photo_id, ai_categories.category_index as category_index").
		Joins("INNER JOIN ai_categories ON ai_categories.category_id = ai_photographs.category_id").
		Where("ai_categories.config_id = ?", configID).
		Scan(&currentPhotos).Error

	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to fetch current photographs for snapshot: %v", err)
	}

	// もし学習に使える画像が1枚もない場合は、ここでロールバックしてエラーを返す（子供への通知用）
	if len(currentPhotos) == 0 {
		tx.Rollback()
		return nil, fmt.Errorf("no photographs found for configuration %s; training cannot start", configID.String())
	}

	// 4. スナップショット用レコードのスライスを組み立て
	snapshots := make([]model.AiTrainingJobSnapshot, len(currentPhotos))
	for i, p := range currentPhotos {
		snapshots[i] = model.AiTrainingJobSnapshot{
			AiTrainingJobID: job.ID, // 確定した Job ID (新規 or 既存) を紐付け
			PhotographID:    p.PhotoID,
			LabelID:         p.CategoryIndex,
		}
	}

	// GORMのバルクインサートで一括保存
	if err := tx.Create(&snapshots).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to bulk insert training job snapshots: %v", err)
	}

	// トランザクション確定
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}

	return &job, nil
}

// AI検索①
func AiSearchDB(database *gorm.DB, courseID uint) ([]model.AiCard, error) {
	var config []model.AiConfiguration
	// クラスIDで箱を検索し、関連するカテゴリなども一緒に読み込む
	err := database.Where("course_id = ?", courseID).Find(&config).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil // 見つからなかった場合はnilを返すのが一般的
		}
		return nil, err // DBエラー
	}
	var cards []model.AiCard
	for i := range config {
		usernam, err := FindUserByID(database, config[i].StudentID.String())
		if err != nil {
			continue
		}
		displayName := usernam.Name
		if strings.Contains(displayName, "-") {
			displayName = strings.Split(displayName, "-")[0]
		}
		var status model.AiTrainingJob
		err = database.Where("config_id = ?", config[i].ProjectUUID).First(&status).Error
		if err != nil {
			continue
		}
		card := model.AiCard{
			ProjectUUID: config[i].ProjectUUID,
			Title:       config[i].Title,
			StudentName: displayName,
			Status:      status.Status,
			UpdatedAt:   config[i].UpdatedAt,
		}
		cards = append(cards, card)
	}

	return cards, nil
}

// IsStudentInCourse: 指定されたユーザーが指定されたコースに参加しているか確認する
func IsStudentInCourse(database *gorm.DB, userID uuid.UUID, courseID uint) (bool, error) {
	var count int64
	// 生徒としてクラスに所属しているかカウント
	err := database.Model(&model.CourseEnrollment{}).
		Where("user_id = ? AND course_id = ?", userID, courseID).
		Count(&count).Error

	if err != nil {
		return false, err // クエリ自体の通信エラーなどはここで即リターン
	}

	// 生徒としての登録が「0件」だった場合、先生としての所属をチェック
	if count == 0 {
		// ターゲットをクラス情報テーブル（model.Course）に切り替え
		err = database.Model(&model.Course{}).
			Where("teacher_id = ? AND id = ?", userID, courseID).
			Count(&count).Error

		if err != nil {
			return false, err
		}
	}

	// count が 0 より大きければ参加していると判定
	return count > 0, nil
}

// Author Check 作成者チェック
func AuthorCheck(database *gorm.DB, userID uuid.UUID, projectID uuid.UUID) (bool, error) {
	var Author model.AiConfiguration

	err := database.Where("project_uuid = ? AND student_id = ?", projectID, userID).First(&Author).Error

	if err == nil {
		// 作成者本人である
		return true, nil
	}
	if err != gorm.ErrRecordNotFound {
		// 作成者本人ではない（別の学生のプロジェクト、または存在しない）
		return false, nil
	}

	return false, nil
}

// AI Generation Status AI作成のステータスを確認。作成中なら作成時間を返す
func AIGenerationStatus(database *gorm.DB, projectID uuid.UUID) (bool, time.Time, error) {
	var latestJob model.AiTrainingJob

	// config_id で絞り込み、一番新しく作られた Job を1件だけ取得する
	err := database.Where("config_id = ?", projectID).Order("created_at DESC").First(&latestJob).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// まだ一度もJobが作られていない＝作成中ではない
			return false, time.Time{}, nil
		}
		// その他の本物のDBエラーは上に投げる
		return false, time.Time{}, err
	}

	// 最新Jobのステータスが "production"であれば、作成中
	if latestJob.Status == "training" {
		return false, latestJob.UpdatedAt, nil
	}

	// "production" 以外はすべて作成可能
	return true, time.Time{}, nil
}

// Change Status ステータスをtrainingに変更
func ChangeStatus(database *gorm.DB, id string) (*model.AiTrainingJob, error) {
	if database == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	var latestJob model.AiTrainingJob

	// ID（主キー）で直接検索
	err := database.Where("id = ?", id).First(&latestJob).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("更新対象の学習ジョブが見つかりません")
		}
		return nil, fmt.Errorf("学習ジョブの取得に失敗しました: %w", err)
	}

	// ステータスを "training" に更新
	err = database.Model(&latestJob).Update("status", "training").Error
	if err != nil {
		return nil, fmt.Errorf("ステータスの更新に失敗しました: %w", err)
	}

	// 更新後のデータをポインタ
	return &latestJob, nil
}

// TrainDataRaw は、DBからJOINで一気に引いてくるための構造体です
type TrainDataRaw struct {
	LabelID        int    // SnapshotのLabelID
	PhotographPath string // AiPhotographの実際のファイルパス
}

func FetchTrainingDataByJobID(db *gorm.DB, jobID uint) (map[int][]string, error) {
	// 安全装置: パニックが起きてもワーカーを落とさない
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC RECOVER] FetchTrainingDataByJobID で深刻なエラーを検知: %v", r)
		}
	}()

	if db == nil {
		return nil, fmt.Errorf("gorm.DB connection is nil")
	}

	var rawResults []TrainDataRaw

	// 中間テーブル (ai_training_job_snapshots) から、対応する写真のパスとラベルIDを一気にJOINで取得
	err := db.Table("ai_training_job_snapshots").
		Select("ai_training_job_snapshots.label_id, ai_photographs.photograph_path").
		Joins("INNER JOIN ai_photographs ON ai_training_job_snapshots.photograph_id = ai_photographs.id").
		Where("ai_training_job_snapshots.ai_training_job_id = ? AND ai_training_job_snapshots.deleted_at IS NULL", jobID).
		Scan(&rawResults).Error

	if err != nil {
		return nil, fmt.Errorf("failed to fetch training data from snapshot by JobID %d: %w", jobID, err)
	}

	// 🌟 ここがポイント：CreateTrainingZip が要求する map[int][]string の形へ変換・集約
	trainingDataMap := make(map[int][]string)

	for _, row := range rawResults {
		if row.PhotographPath == "" {
			continue // 空のパスはスキップ
		}
		// ラベルIDをキーにして、パスの配列にappendしていく
		trainingDataMap[row.LabelID] = append(trainingDataMap[row.LabelID], row.PhotographPath)
	}

	return trainingDataMap, nil
}

func CreateTrainingJob(database *gorm.DB, configID uuid.UUID) error {
	if database == nil {
		return fmt.Errorf("database connection is nil")
	}

	tx := database.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	job := &model.AiTrainingJob{
		ConfigID: configID,
		Status:   "pending",
	}

	result := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "config_id"},
			{Name: "status"},
		},
		DoNothing: true,
	}).Create(job)

	if result.Error != nil {
		tx.Rollback()
		return result.Error
	}

	// 既に誰かが作っていた
	if result.RowsAffected == 0 {
		err := tx.Where(
			"config_id = ? AND status = ?",
			configID,
			"pending",
		).First(job).Error

		if err != nil {
			tx.Rollback()
			return err
		}

		fmt.Printf("[Debug] 既存Job(ID:%d)を取得\n", job.ID)
	} else {
		fmt.Printf("[Debug] 新規Job(ID:%d)を作成\n", job.ID)
	}

	if err := tx.Commit().Error; err != nil {
		return err
	}

	return nil
}
