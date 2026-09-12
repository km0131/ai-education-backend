package db

import (
	"ai-education/backend/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CreateContentReport は公開プレビュー(preview.a-kiis.com)についての通報を
// 1件記録する(POST /api/v2/report、report_handler.go)。
func CreateContentReport(tx *gorm.DB, reporterUserID uuid.UUID, publishSlug, reason string) error {
	return tx.Create(&model.ContentReport{
		ReporterUserID: reporterUserID,
		PublishSlug:    publishSlug,
		Reason:         reason,
	}).Error
}
