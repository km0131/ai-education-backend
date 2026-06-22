package db

import (
	"ai-education/backend/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var Tx *gorm.DB

// GetOrCreateConfig: プロジェクトの「箱」を取得または自動作成
func GetOrCreateConfig(tx *gorm.DB, userID uuid.UUID, courseID uint, title string) (*model.AiConfiguration, error) {
	// 既存のプロジェクトが存在すれば論理削除
	err := tx.Model(&model.AiConfiguration{}).
		Where("student_id = ? AND course_id = ?", userID, courseID).
		Delete(&model.AiConfiguration{}).Error
	if err != nil {
		return nil, err
	}
	// UUIDの生成
	newID := uuid.New()
	// 新規作成
	newConfig := &model.AiConfiguration{
		ProjectUUID: newID,
		StudentID:   userID,
		CourseID:    courseID,
		Title:       title,
		IsShared:    true,
	}

	if err := tx.Create(newConfig).Error; err != nil {
		return nil, err
	}

	return newConfig, nil
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
func GetPhotographByID(id string) (*model.AiPhotograph, error) {
	var photo model.AiPhotograph
	result := Tx.Where("id = ?", id).First(&photo)

	return &photo, result.Error
}

// 分析結果を保存
func UpdatePhotoAnalysis(photoID int, data model.AnalysisData) error {
	// model.AiPhotograph 構造体が Sharpness と DiversityVector を保持していると仮定
	err := Tx.Model(&model.AiPhotograph{}).
		Where("id = ?", photoID).
		Updates(model.AiPhotograph{
			Saturation:      data.Saturation,
			Brightness:      data.Brightness,
			Sharpness:       data.Sharpness,
			DiversityVector: data.DiversityVector, // JSONB型として保存
			IsAnalyzed:      true,
		}).Error

	return err
}
