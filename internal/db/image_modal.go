package db

import (
	"ai-education/backend/internal/model"
	"fmt"
	"log"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func ImageAcquisitionDB(database *gorm.DB, userID uuid.UUID, teacher bool, projectID uuid.UUID) (map[string]*model.CategoryPhotosMap, error) {
	if teacher == false {
		author, err := AuthorCheck(database, userID, projectID)
		if err != nil {
			log.Printf("[ERROR] 画像の取得を失敗しました。: %v", err)
			return nil, err
		}
		if !author {
			log.Printf("[ERROR] 画像を見る権限が有りません: %v", err)
			return nil, fmt.Errorf("画像を見る権限が有りません")
		}
	}
	type ScanResult struct {
		CategoryID    string
		CategoryTitle string
		CategoryIndex uint
		PhotoID       uint
		PhotoPath     string
	}

	var rows []ScanResult

	err := database.Table("ai_photographs").
		// 🆕 Select句に「ai_categories.category_index」を追加（カラム名が異なる場合は修正してください、例: category_id の数値版など）
		Select("ai_categories.category_id, ai_categories.title as category_title, ai_categories.category_index, ai_photographs.id as photo_id, ai_photographs.photograph_path as photo_path").
		Joins("JOIN ai_training_job_snapshots ON ai_training_job_snapshots.photograph_id = ai_photographs.id").
		Joins("JOIN ai_training_jobs ON ai_training_jobs.id = ai_training_job_snapshots.ai_training_job_id").
		// カテゴリ名を取得するために AiCategory を結合
		Joins("JOIN ai_categories ON ai_categories.category_id = ai_photographs.category_id").
		Where("ai_training_jobs.config_id = ? AND ai_training_jobs.is_current = ?", projectID, true).
		Where("ai_training_jobs.deleted_at IS NULL AND ai_training_job_snapshots.deleted_at IS NULL AND ai_photographs.deleted_at IS NULL AND ai_categories.deleted_at IS NULL").
		Scan(&rows).Error

	if err != nil {
		return nil, err
	}

	// 取得したレコードを CategoryID ごとにマップへ詰め替える
	resultMap := make(map[string]*model.CategoryPhotosMap)

	for _, row := range rows {
		// カテゴリ名が空の場合はデフォルト値を設定（安全のため）
		titleKey := row.CategoryTitle
		if titleKey == "" {
			titleKey = "未設定のラベル"
		}
		//　まだマップにこの「カテゴリタイトル」が存在しない場合は初期化
		if _, exists := resultMap[titleKey]; !exists {
			resultMap[titleKey] = &model.CategoryPhotosMap{
				// フロント側で画像追加（Upload）や削除をするときにIDが必要になるため、
				// 構造体の中に CategoryID を紐づけて一緒に返してあげると親切です
				CategoryID:    row.CategoryID,
				CategoryIndex: row.CategoryIndex,
				Title:         titleKey,
				Photos:        []model.PhotoInfo{},
			}
		}
		// 画像情報がある場合は配列に追加
		if row.PhotoID != 0 {
			resultMap[titleKey].Photos = append(resultMap[titleKey].Photos, model.PhotoInfo{
				ID:   row.PhotoID,
				Path: row.PhotoPath,
			})
		}
	}

	return resultMap, nil
}

func UpTrainingJobSnapshot(database *gorm.DB, photo *model.AiPhotograph, projectUUID uuid.UUID) error {
	var currentJob model.AiTrainingJob
	err := database.Where("config_id = ? AND is_current = ? AND deleted_at IS NULL", projectUUID, true).First(&currentJob).Error
	if err != nil {
		log.Printf("[ERROR] ジョブの取得に失敗しました。: %v", err)
		return err
	}
	var aicategory model.AiCategory
	err = database.Where("config_id = ? AND category_id = ? ", projectUUID, photo.CategoryID).First(&aicategory).Error
	if err != nil {
		log.Printf("[ERROR] ラベルの取得に失敗しました。: %v", err)
		return err
	}
	newSnapshot := model.AiTrainingJobSnapshot{
		AiTrainingJobID: currentJob.ID,            // 検索で見つかったJobの主キーID
		PhotographID:    photo.ID,                 // すでにある写真ID
		LabelID:         aicategory.CategoryIndex, // フロントから届いたcategory_indexの数値
	}
	if err := database.Create(&newSnapshot).Error; err != nil {
		log.Printf("[ERROR] スナップショットに画像を追加しました。: %v", err)
		return err
	}
	return nil
}

func DeletedImageDB(database *gorm.DB, photographID uint) error {
	err := database.Where("photograph_id = ?", photographID).Delete(&model.AiTrainingJobSnapshot{}).Error
	if err != nil {
		log.Printf("[ERROR] スナップショットの論理削除に失敗しました: %v", err)
		return err
	}
	return nil
}
