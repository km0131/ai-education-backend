package service

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/worker"
	"fmt"
	"image"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"time"

	"github.com/disintegration/imaging"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// アップロード画像をリサイズする際の長辺の上限(px)。
// このプロジェクトで使う3モデル(CNN/ViT/MobileViT-v2)の最大入力サイズ(380px)を
// 十分カバーできるサイズとして設定している。
const maxImageLongSide = 512

func GenerateNewFilename(originalFilename string) string {
	// 元ファイルの拡張子を取得 (.jpg など)
	ext := filepath.Ext(originalFilename)

	// 新しいUUIDを生成して拡張子を結合
	return uuid.New().String() + ext
}

// saveMultipartFile は、アップロードされたファイルを無変換のままdestPathへ保存する。
func saveMultipartFile(fh *multipart.FileHeader, destPath string) error {
	src, err := fh.Open()
	if err != nil {
		return fmt.Errorf("failed to open uploaded file: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to save file to disk: %w", err)
	}
	return nil
}

// resizeToMaxLongSide は、長辺がmaxImageLongSideを超える場合のみアスペクト比を維持してリサイズする。
func resizeToMaxLongSide(img image.Image) image.Image {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if max(width, height) <= maxImageLongSide {
		return img
	}
	if width >= height {
		return imaging.Resize(img, maxImageLongSide, 0, imaging.Linear)
	}
	return imaging.Resize(img, 0, maxImageLongSide, imaging.Linear)
}

// decodeWithFallback は、まず通常のimaging.Openでのデコードを試みる。
// それが失敗した場合のみ、拡張子からHEIC/HEIF・RAW(CR2/CR3)と判断できるファイルに限って
// 外部コマンド(heif-convert/exiftool)によるフォールバック変換を行う。
// フロントエンドで変換済みのJPEGが大半を占めるため、この経路は「フロントで変換できなかった場合」
// のみ通る想定であり、ラズパイへの負荷は最小限に抑えられる。
func decodeWithFallback(path string, originalFilename string) (image.Image, error) {
	img, err := imaging.Open(path)
	if err == nil {
		return img, nil
	}

	switch {
	case isHeicFilename(originalFilename):
		converted, convErr := convertHeicToJpeg(path)
		if convErr != nil {
			return nil, fmt.Errorf("HEIC画像の変換に失敗しました: %w", convErr)
		}
		defer os.Remove(converted)
		return imaging.Open(converted)
	case isRawFilename(originalFilename):
		preview, prevErr := extractRawPreviewJpeg(path)
		if prevErr != nil {
			return nil, fmt.Errorf("RAW画像のプレビュー抽出に失敗しました: %w", prevErr)
		}
		defer os.Remove(preview)
		return imaging.Open(preview)
	default:
		return nil, fmt.Errorf("failed to decode image (unsupported format): %w", err)
	}
}

// saveOriginalAndResizedImage は、アップロードされたファイルをoriginalSavePathへ無変換で保存する。
// resizedFileが渡された場合(フロントエンドで既に長辺512px以下にリサイズ済みの場合)は、それをそのままsavePathへ保存する
// (長辺が512pxを超えていた場合のみ、念のためサーバー側でも縮小するフォールバックを行う)。
// resizedFileがnilの場合(フロントでのリサイズに失敗した場合など)は、従来通りオリジナルから
// サーバー側でリサイズ・JPEG品質85で保存する。
func saveOriginalAndResizedImage(file *multipart.FileHeader, resizedFile *multipart.FileHeader, originalSavePath, savePath string) error {
	if err := os.MkdirAll(filepath.Dir(originalSavePath), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
		return err
	}

	// 1. アップロードされたファイルをオリジナルとして無変換で保存する
	if err := saveMultipartFile(file, originalSavePath); err != nil {
		return err
	}

	if resizedFile != nil {
		// 2a. フロントエンドがリサイズ済み画像を送ってきた場合は、それをそのまま保存する
		if err := saveMultipartFile(resizedFile, savePath); err != nil {
			return err
		}

		img, err := imaging.Open(savePath)
		if err != nil {
			return fmt.Errorf("failed to decode resized image: %w", err)
		}
		bounds := img.Bounds()
		if max(bounds.Dx(), bounds.Dy()) <= maxImageLongSide {
			return nil
		}

		// フロント側のリサイズが不十分だった場合のフォールバック
		img = resizeToMaxLongSide(img)
		if err := imaging.Save(img, savePath, imaging.JPEGQuality(85)); err != nil {
			return fmt.Errorf("failed to save resized image: %w", err)
		}
		return nil
	}

	// 2b. リサイズ済み画像が送られてこなかった場合のフォールバック: オリジナルから生成する
	img, err := decodeWithFallback(originalSavePath, file.Filename)
	if err != nil {
		return err
	}
	img = resizeToMaxLongSide(img)
	if err := imaging.Save(img, savePath, imaging.JPEGQuality(85)); err != nil {
		return fmt.Errorf("failed to save resized image: %w", err)
	}
	return nil
}

func SaveAndAnalyze(database *gorm.DB, userID uuid.UUID, rot model.ImageUploadRequest, file *multipart.FileHeader, resizedFile *multipart.FileHeader) (*model.AiPhotograph, error) {
	// ファイル名生成(リサイズ版とオリジナル版で同じUUIDを共有し、拡張子だけ変える)
	baseName := uuid.New().String()
	originalFilename := baseName + filepath.Ext(file.Filename)
	resizedFilename := baseName + ".jpg"

	// リサイズ版は今までと全く同じパス。オリジナルはディレクトリ名だけ差し替えたパスに保存する
	savePath := fmt.Sprintf("images/ai_photogrph/%s/%s", userID.String(), resizedFilename)
	originalSavePath := fmt.Sprintf("images/ai_photogrph_original/%s/%s", userID.String(), originalFilename)

	if err := saveOriginalAndResizedImage(file, resizedFile, originalSavePath, savePath); err != nil {
		return nil, err
	}

	var photo *model.AiPhotograph
	var targetConfigUUID uuid.UUID

	// トランザクションでデータの整合性を100%保証する
	err := database.Transaction(func(tx *gorm.DB) error {
		parsedSessionID, err := uuid.Parse(rot.UploadSessionID)
		// プロジェクト(箱)を作成
		config, err := db.GetOrCreateConfig(tx, userID, rot.CourseID, rot.Title, parsedSessionID)
		if err != nil {
			return err
		}

		targetConfigUUID = config.ProjectUUID

		// ラベル(カテゴリ)を最新版として作成
		category, err := db.CreateCategoryWithHistory(tx, config.ProjectUUID, int(rot.CategoryID), rot.CategoryTitle)
		if err != nil {
			return err
		}

		// 学習データを保存
		photo, err = db.CreatePhotograph(tx, category.CategoryID, userID, savePath)
		return err
	})

	if err != nil {
		log.Printf("[ERROR] SaveAndAnalyze トランザクション失敗: %v", err)
		return nil, err
	}

	// 画像評価キューに登録
	worker.Scheduler.Enqueue(&worker.GPUJobRequest{
		Kind:     worker.JobKindAnalysis,
		Priority: worker.PriorityAnalysis,
		PhotoID:  photo.ID,
	})

	// AIカート作成

	log.Printf("[INFO] TrainingJob 作成を試みます。ConfigID: %s", targetConfigUUID)
	err = db.CreateTrainingJob(database, targetConfigUUID)
	if err != nil {
		log.Printf("[ERROR] CreateTrainingJob 失敗: %v", err)
		return nil, fmt.Errorf("failed to register training job: %w", err)
	}

	return photo, nil
}

func AICreation(database *gorm.DB, userId uuid.UUID, teacher bool, projectId uuid.UUID) (time.Time, error) {
	if teacher == false {
		author, err := db.AuthorCheck(database, userId, projectId)
		if err != nil {
			log.Printf("[ERROR] AI作成の作成に失敗しました。: %v", err)
			return time.Time{}, fmt.Errorf("AI作成の作成に失敗しました。: %w", err)
		}
		if !author {
			log.Printf("[ERROR] AI作成の作成権限が有りません。: %v", err)
			return time.Time{}, fmt.Errorf("AI作成の作成権限が有りません。: %w", err)
		}
	}
	// ステータスを確認して作成中ではないかチェック
	status, sttime, err := db.AIGenerationStatus(database, projectId)
	if err != nil {
		log.Printf("[ERROR] AI作成の作成に失敗しました。: %v", err)
		return time.Time{}, fmt.Errorf("AI作成の作成に失敗しました。: %w", err)
	}
	if !status {
		log.Printf("[WARN] すでにAIを作成中です。プロジェクトID: %s, 開始時間: %v", projectId, sttime)
		return sttime, fmt.Errorf("現在AIを作成中です（開始時間: %s）。しばらくお待ちください", sttime)
	}

	// ─── ここからAI作成用の処理 ───
	trainingJob, err := db.CreateTrainingJobWithSnapshot(database, projectId)
	if err != nil {
		log.Printf("[ERROR] 学習ジョブの作成に失敗: %v", err)
		return time.Time{}, fmt.Errorf("学習データのまとめ処理に失敗しました: %w", err)
	}
	// AI作成キューへJob IDを投入（GPUは1系統のためテスト・分析と同じスケジューラで直列化する）
	worker.Scheduler.Enqueue(&worker.GPUJobRequest{
		Kind:     worker.JobKindTrain,
		Priority: worker.PriorityTrain,
		JobID:    trainingJob.ID,
	})

	log.Printf("[INFO] AI作成ジョブをキューに登録しました。JobID: %d", trainingJob.ID)
	return time.Time{}, nil
}
