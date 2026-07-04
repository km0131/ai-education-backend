package db

import (
	"ai-education/backend/internal/model"
	"fmt"
	"log"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func LabelGet(database *gorm.DB, userID uuid.UUID, teacher bool, projectID uuid.UUID) ([]model.CategoryResponse, error) {
	if teacher == false {
		author, err := AuthorCheck(database, userID, projectID)
		if err != nil {
			log.Printf("[ERROR] AI作成の作成に失敗しました。: %v", err)
			return nil, err
		}
		if !author {
			log.Printf("[ERROR] AI作成の作成権限が有りません。: %v", err)
			return nil, fmt.Errorf("AI作成の作成権限が有りません")
		}
	}
	// データベースから全件取得（先ほどの処理）
	var collapsedCategories []model.AiCategory
	err := database.Where("config_id = ?", projectID).
		Select("category_index, title,MAX(explanation) as explanation").
		Group("category_index, title"). // この2つが同じものをグループ化
		Order("category_index ASC").    // インデックス順に並び替え
		Find(&collapsedCategories).Error

	if err != nil {
		log.Printf("[ERROR] カテゴリリストの集約に失敗しました: %v", err)
		return nil, err
	}

	var response []model.CategoryResponse
	for _, l := range collapsedCategories {
		response = append(response, model.CategoryResponse{
			CategoryIndex: l.CategoryIndex,
			Title:         l.Title,
			Explanation:   l.Explanation,
		})
	}

	return response, nil
}

func LabelCreation(database *gorm.DB, userID uuid.UUID, teacher bool, projectID uuid.UUID, explanation map[int]string) error {
	if teacher == false {
		author, err := AuthorCheck(database, userID, projectID)
		if err != nil {
			log.Printf("[ERROR] AI作成の作成に失敗しました。: %v", err)
			return err
		}
		if !author {
			log.Printf("[ERROR] AI作成の作成権限が有りません。: %v", err)
			return fmt.Errorf("AI作成の作成権限が有りません")
		}
	}
	// トランザクションを開始する（一括処理を安全に行うため）
	tx := database.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	for categoryIndex, text := range explanation {
		// config_id と category_index が一致するレコードの explanation 列を更新
		err := tx.Model(&model.AiCategory{}).
			Where("config_id = ? AND category_index = ?", projectID, categoryIndex).
			Update("explanation", text).Error

		if err != nil {
			tx.Rollback() // どこか1つでも失敗したらすべて巻き戻す
			log.Printf("[ERROR] 説明文の登録（上書き）に失敗しました (Index: %d): %v", categoryIndex, err)
			return err
		}
	}
	if err := tx.Commit().Error; err != nil {
		log.Printf("[ERROR] トランザクションのコミットに失敗しました: %v", err)
		return err
	}

	return nil

}

func UpLabelDB(database *gorm.DB, userID uuid.UUID, teacher bool, projectID uuid.UUID, oldLabelName string, newLabelName string) (int64, error) {
	if teacher == false {
		author, err := AuthorCheck(database, userID, projectID)
		if err != nil {
			log.Printf("[ERROR] AI作成の作成に失敗しました。: %v", err)
			return 0, err
		}
		if !author {
			log.Printf("[ERROR] ラベル修正の権限が有りません。: %v", err)
			return 0, fmt.Errorf("ラベル修正の権限が有りません")
		}
	}
	if newLabelName == "" || oldLabelName == newLabelName {
		return 0, nil
	}
	result := database.Model(&model.AiCategory{}).
		Where("config_id = ? AND title = ?", projectID, oldLabelName).
		Update("title", newLabelName)

	if result.Error != nil {
		return 0, fmt.Errorf("failed to update test image labels: %w", result.Error)
	}
	log.Printf("[Info] ラベル変更完了: プロジェクトID %d において '%s' から '%s' へ %d 件変更されました",
		projectID, oldLabelName, newLabelName, result.RowsAffected)

	// 影響のあった件数（何件書き換わったか）を返す
	return result.RowsAffected, nil
}
