package db

import (
	"ai-education/backend/internal/model"
	"gorm.io/gorm"
)

// SaveSystemLog はシステムログをDBに保存します。
func SaveSystemLog(tx *gorm.DB, level, action, message, detail string, userID *uint) error {
	if tx == nil {
		return nil
	}

	entry := model.SystemLog{
		Level:   level,
		UserID:  userID,
		Action:  action,
		Message: message,
		Detail:  detail,
	}

	return tx.Create(&entry).Error
}
