package db

import (
	"ai-education/backend/internal/model"
	"fmt"
	"log"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TrainingCurveDB(db *gorm.DB, projectID uuid.UUID) (*model.AiTrainingJob, error) {
	var trainingCurve model.AiTrainingJob
	err := db.Where("config_id = ? AND is_current = ?", projectID, true).Find(&trainingCurve).Error
	if err != nil {
		log.Println(err)
		return nil, err
	}
	return &trainingCurve, nil
}

func TestResultsDB(db *gorm.DB, projectID uuid.UUID) (map[string]float64, error) {
	trainingCurve, err := TrainingCurveDB(db, projectID)
	if err != nil {
		return nil, err
	}
	var testJob model.StudentTestJob
	if err := db.Where("training_job_id = ?", trainingCurve.ID).
		Preload("Models").
		Order("created_at DESC").
		First(&testJob).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch test job: %w", err)
	}
	accuracyByModel := make(map[string]float64, len(testJob.Models))
	for _, m := range testJob.Models {
		accuracyByModel[m.ModelName] = m.Accuracy
	}

	return accuracyByModel, nil
}

// TestImageResultEntry: 1枚の画像に対する結果
type TestImageResultEntry struct {
	TestImageID      uint    `json:"test_image_id"`
	ImageURL         string  `json:"image_url"`          // ★追加：画像の配信URL
	CorrectLabelName string  `json:"correct_label_name"` // ★追加：正解ラベル名
	PredictedLabelID int     `json:"predicted_label_id"`
	Confidence       float64 `json:"confidence"`
	IsCorrect        bool    `json:"is_correct"`
}

// TestResultsImageDB: テスト結果を「モデル名 -> 画像ごとの結果一覧」の形でまとめて返す
func TestResultsImageDB(db *gorm.DB, projectID uuid.UUID) (map[string][]TestImageResultEntry, error) {
	trainingCurve, err := TrainingCurveDB(db, projectID)
	if err != nil {
		return nil, err
	}
	// StudentTestJob本体 + 紐づくStudentTestJobModel一覧を取得
	var testJob model.StudentTestJob
	if err := db.Where("training_job_id = ?", trainingCurve.ID).
		Preload("Models").
		Order("created_at DESC").
		First(&testJob).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch test job: %w", err)
	}
	if len(testJob.Models) == 0 {
		return map[string][]TestImageResultEntry{}, nil
	}
	// StudentTestJobModel.ID -> ModelName の対応表を作る
	modelNameByID := make(map[uint]string, len(testJob.Models))
	jobModelIDs := make([]uint, 0, len(testJob.Models))
	for _, m := range testJob.Models {
		modelNameByID[m.ID] = m.ModelName
		jobModelIDs = append(jobModelIDs, m.ID)
	}
	// 該当する全モデルの画像結果(スナップショット)を一括取得
	var snapshots []model.StudentTestResultSnapshot
	if err := db.Where("student_test_job_model_id IN ?", jobModelIDs).
		Find(&snapshots).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch test result snapshots: %w", err)
	}
	if len(snapshots) == 0 {
		return map[string][]TestImageResultEntry{}, nil
	}
	// TestImageID一覧を集めて、画像情報(URL・正解ラベル名)を一括取得
	imageIDSet := make(map[uint]struct{})
	for _, s := range snapshots {
		imageIDSet[s.TestImageID] = struct{}{}
	}
	imageIDs := make([]uint, 0, len(imageIDSet))
	for id := range imageIDSet {
		imageIDs = append(imageIDs, id)
	}
	var testImages []model.TestImage
	if err := db.Where("id IN ?", imageIDs).Find(&testImages).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch test images: %w", err)
	}
	// TestImageID -> TestImage の対応表を作る（URL・正解ラベル名の参照用）
	imageByID := make(map[uint]model.TestImage, len(testImages))
	for _, img := range testImages {
		imageByID[img.ID] = img
	}
	// モデル名ごとにグルーピング（画像URL・正解ラベル名も付与）
	resultsByModel := make(map[string][]TestImageResultEntry)
	for _, s := range snapshots {
		modelName := modelNameByID[s.StudentTestJobModelID]
		img := imageByID[s.TestImageID]

		resultsByModel[modelName] = append(resultsByModel[modelName], TestImageResultEntry{
			TestImageID:      s.TestImageID,
			ImageURL:         img.ImageURL,
			CorrectLabelName: img.CorrectLabelName,
			PredictedLabelID: s.PredictedLabelID,
			Confidence:       s.Confidence,
			IsCorrect:        s.IsCorrect,
		})
	}

	return resultsByModel, nil
}
