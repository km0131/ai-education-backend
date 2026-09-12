package db

import (
	"ai-education/backend/internal/model"

	"gorm.io/gorm"
)

// ListFeatureTypes は feature_types の全件を返す(id昇順)。
// クラス作成フォームの選択肢は、これを動的に取得して描画する想定。
func ListFeatureTypes(tx *gorm.DB) ([]model.FeatureType, error) {
	var types []model.FeatureType
	err := tx.Order("id ASC").Find(&types).Error
	return types, err
}

// FeatureTypeExists は指定IDのfeature_typeが存在するかを返す。
// クラス作成時のfeature_type_idバリデーションに使う。
func FeatureTypeExists(tx *gorm.DB, id uint) (bool, error) {
	var count int64
	err := tx.Model(&model.FeatureType{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}
