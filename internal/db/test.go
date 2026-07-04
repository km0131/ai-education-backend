package db

import (
	"ai-education/backend/internal/model"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// テストデータが存在しているかチェック
func TestDataCheck(db *gorm.DB, courseID uint, batchID uuid.UUID) (bool, error) {
	var count int64
	err := db.Model(&model.TestImage{}).Where("course_id = ? ", courseID).Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("failed to check test data existence: %w", err)
	}
	// クラスIDのデータがまだ1件も無いなら、無条件で登録
	if count == 0 {
		return true, nil
	}
	// クラスIDが存在する場合、送られてきた batch_id がすでにそのクラスに含まれているか確認
	var batchCount int64
	err = db.Model(&model.TestImage{}).
		Where("course_id = ? AND batch_id = ?", courseID, batchID).
		Count(&batchCount).Error
	if err != nil {
		return false, fmt.Errorf("failed to check batch existence: %w", err)
	}
	// クラスIDが有り、かつ同じ batch_id のデータが1件以上あるなら登録OK
	if batchCount > 0 {
		return true, nil
	}
	// クラスIDが有るのに、同じ batch_id が見つからない（違うID）なら登録不可
	return false, fmt.Errorf("すでに登録されているので登録出来ません")
}

// テスト画像保存用DB
func CreatingTestDatasetDB(db *gorm.DB, courseID uint, imageURL string, batchID uuid.UUID, correctLabelName string) error {
	// トランザクション処理の開始
	err := db.Transaction(func(tx *gorm.DB) error {
		// 挿入するデータの構造体を組み立てる
		testImage := model.TestImage{
			CourseID:         courseID,
			ImageURL:         imageURL,
			BatchID:          batchID,
			CorrectLabelName: correctLabelName,
		}
		// DBにレコードを追加
		if err := tx.Create(&testImage).Error; err != nil {
			// エラーを返すと自動的にロールバックされます
			return err
		}
		return nil
	})
	return err
}

type TestImageResponse struct {
	ID       uint   `json:"id"`
	ImageURL string `json:"image_url"`
}

// テスト画像を取得
func GetImageDB(db *gorm.DB, courseID uint) (map[uint]map[uuid.UUID]map[string][]TestImageResponse, error) {
	var testImages []model.TestImage
	err := db.Where("course_id = ?", courseID).Find(&testImages).Error
	if err != nil {
		// それ以外の本物のDBエラー
		return nil, fmt.Errorf("failed to get test image: %w", err)
	}

	result := make(map[uint]map[uuid.UUID]map[string][]TestImageResponse)

	for _, img := range testImages {
		// 2. 第1階層（CourseID）の nil チェックと初期化
		if result[img.CourseID] == nil {
			result[img.CourseID] = make(map[uuid.UUID]map[string][]TestImageResponse)
		}

		// 3. 第2階層（BatchID）の nil チェックと初期化
		if result[img.CourseID][img.BatchID] == nil {
			result[img.CourseID][img.BatchID] = make(map[string][]TestImageResponse)
		}

		// 4. 正しく3つのキーを指定してデータを append
		result[img.CourseID][img.BatchID][img.CorrectLabelName] = append(
			result[img.CourseID][img.BatchID][img.CorrectLabelName],
			TestImageResponse{
				ID:       img.ID,
				ImageURL: img.ImageURL,
			},
		)
	}
	//　取得成功時は、構造体のポインタを返す
	return result, nil
}

// 画像削除
func DeleteImageDB(db *gorm.DB, ID int) error {
	if err := db.Where("id = ?", ID).Delete(&model.TestImage{}).Error; err != nil {
		return err
	}
	return nil
}

// リストを取得
func GetTestLabelDB(db *gorm.DB, courseID uint) ([]string, error) {
	var testImages []model.TestImage
	err := db.Where("course_id = ?", courseID).Find(&testImages).Error
	if err != nil {
		// DBエラー
		return nil, fmt.Errorf("failed to get test image: %w", err)
	}

	// map を使ってラベル名の重複を排除
	labelMap := make(map[string]bool)
	for _, img := range testImages {
		if img.CorrectLabelName != "" { // 空文字でなければマップに登録
			labelMap[img.CorrectLabelName] = true
		}
	}

	// マップのキーを取り出して、まとめられたリスト（スライス）を作成
	labels := make([]string, 0, len(labelMap))
	for label := range labelMap {
		labels = append(labels, label)
	}

	// まとめたリストと nil エラーを返す
	return labels, nil

}

// リストを変更
func UpTestLabelDB(db *gorm.DB, courseID uint, oldLabelName string, newLabelName string) (int64, error) {
	// 空文字への変更や、変更前後が同じ場合は処理をスキップ
	if newLabelName == "" || oldLabelName == newLabelName {
		return 0, nil
	}
	result := db.Model(&model.TestImage{}).
		Where("course_id = ? AND correct_label_name = ?", courseID, oldLabelName).
		Update("correct_label_name", newLabelName)

	if result.Error != nil {
		return 0, fmt.Errorf("failed to update test image labels: %w", result.Error)
	}
	log.Printf("[Info] ラベル変更完了: クラスID %d において '%s' から '%s' へ %d 件変更されました",
		courseID, oldLabelName, newLabelName, result.RowsAffected)

	// 影響のあった件数（何件書き換わったか）を返す
	return result.RowsAffected, nil
}

// テストラベルの中間テーブルを取得
func GetStudentTestMapping(db *gorm.DB, projectid uuid.UUID) ([]model.StudentTestMapping, error) {
	var testLabel []model.StudentTestMapping
	err := db.Where("project_uuid = ?", projectid).Find(&testLabel).Error
	if err != nil {
		log.Printf("[Info] ラベルの中間テーブルの取得に失敗しました。")
		return nil, err
	}
	return testLabel, nil
}

// 生徒と先生のラベルの中間テーブルをUP
func UpStudentTestLabelDB(db *gorm.DB, req model.SaveMappingRequest, userID uuid.UUID, teacher bool) error {
	if teacher == false {
		author, err := AuthorCheck(db, userID, req.ProjectUUID)
		if err != nil {
			log.Printf("[ERROR] AI作成の作成に失敗しました。: %v", err)
			return err
		}
		if !author {
			log.Printf("[ERROR] ラベル修正の権限が有りません。: %v", err)
			return fmt.Errorf("ラベル修正の権限が有りません")
		}
	}
	var dbMappings []model.StudentTestMapping
	for _, m := range req.Mappings {
		dbMappings = append(dbMappings, model.StudentTestMapping{
			ProjectUUID:      req.ProjectUUID,
			CourseID:         req.CourseID,
			StudentLabelName: m.StudentLabelName,
			TeacherLabelName: m.TeacherLabelName,
		})
	}
	err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_uuid"}, {Name: "student_label_name"}},  // 競合を判定するカラム
		DoUpdates: clause.AssignmentColumns([]string{"teacher_label_name", "updated_at"}), // 競合時に更新するカラム
	}).Create(&dbMappings).Error

	if err != nil {
		log.Printf("[Info] ラベルの中間テーブルの更新に失敗しました。")
		return err
	}

	return nil

}

// テストステータスを確認
func TestStatus(db *gorm.DB, projectID uuid.UUID, userID uuid.UUID) (*model.StudentTestJob, *time.Time, error) {
	var trainingJob model.AiTrainingJob
	// projectId (ConfigID) を元に、現在有効なAIモデル（IsCurrent = true）を取得する
	err := db.Where("config_id = ? AND is_current = ? AND status = ?", projectID, true, "success").
		Order("version DESC"). // 万が一のため最新のもの
		First(&trainingJob).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errors.New("有効な学習済みモデル（バージョン）が見つかりません。先に学習を行ってください。")
		}
		return nil, nil, err
	}
	var existingJob model.StudentTestJob
	// 該当するモデル（TrainingJobID）で、現在実行中（running）のテストジョブがないか確認
	err = db.Where("training_job_id = ? AND status = ?", trainingJob.ID, "running").
		Order("updated_at DESC").
		First(&existingJob).Error
	// 💡 すでに running のジョブが存在する場合 ➔ そのまま構造体と UpdatedAt（UP時間）を返す
	if err == nil {
		return &existingJob, &existingJob.UpdatedAt, nil
	}
	// running のジョブがなければ、新規に "pending" ステータスで作成する
	newJob := model.StudentTestJob{
		UserID:        userID,
		TrainingJobID: trainingJob.ID,
		Status:        "pending", // 準備中
		TotalAccuracy: 0.0,
	}
	if err := db.Create(&newJob).Error; err != nil {
		return nil, nil, err
	}
	// 新規作成なので、UP時間は不要（nil）で返す
	return &newJob, nil, nil
}

// GetTestImagesByProject はプロジェクトに紐づくテスト画像一覧を取得します
func GetTestImagesByProject(db *gorm.DB, courseid uint) ([]model.TestImage, error) {
	var images []model.TestImage
	// project_id に紐づくテスト画像を全件取得
	err := db.Where("course_id = ?", courseid).Find(&images).Error
	return images, err
}

// GetModelPathsByTrainingJob は指定された学習ジョブのモデルのパスを取得します
func GetModelPathsByTrainingJob(db *gorm.DB, trainingJobID uuid.UUID) (*model.AiTrainingJob, error) {
	var job model.AiTrainingJob
	err := db.Where("config_id = ? AND is_current = ?", trainingJobID, true).First(&job).Error
	return &job, err
}
