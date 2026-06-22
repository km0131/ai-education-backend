package service

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/worker"
	"fmt"
	"mime/multipart"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GenerateNewFilename(originalFilename string) string {
	// 元ファイルの拡張子を取得 (.jpg など)
	ext := filepath.Ext(originalFilename)

	// 新しいUUIDを生成して拡張子を結合
	return uuid.New().String() + ext
}

func SaveAndAnalyze(database *gorm.DB, userID uuid.UUID, courseID, categoryID uint, title string, file *multipart.FileHeader) (*model.AiPhotograph, error) {
	// ファイル名生成
	filename := uuid.New().String() + filepath.Ext(file.Filename)
	savePath := fmt.Sprintf("images/ai_photogrph/%s/%s", userID.String(), filename)

	// ディレクトリ作成とファイル保存はここで実行
	if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
		return nil, err
	}

	var photo *model.AiPhotograph

	// トランザクションでデータの整合性を100%保証する
	err := database.Transaction(func(tx *gorm.DB) error {
		// プロジェクト(箱)を作成
		config, err := db.GetOrCreateConfig(tx, userID, courseID, title)
		if err != nil {
			return err
		}

		// ラベル(カテゴリ)を最新版として作成
		category, err := db.CreateCategoryWithHistory(tx, config.ProjectUUID, int(categoryID), title)
		if err != nil {
			return err
		}

		// 学習データを保存
		photo, err = db.CreatePhotograph(tx, category.CategoryID, userID, savePath)
		return err
	})

	// キューに登録
	worker.AnalysisQueue <- photo.ID

	return photo, err
}
