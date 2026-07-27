package service

import (
	"log"

	"ai-education/backend/internal/model"

	"github.com/disintegration/imaging"
	"gorm.io/gorm"
)

// fallbackJob は、HTTPリクエストの外側で実行するHEIC/RAWフォールバック変換1件分の情報。
// AiPhotograph(学習データ)とTestImage(テスト画像)の両方から使えるよう、DB更新はコールバックで吸収する。
type fallbackJob struct {
	Database         *gorm.DB
	OriginalPath     string
	OriginalFilename string
	SavePath         string
	// UpdateStatus はDB上のConversionStatus/ConversionErrorを更新するコールバック。
	// 成功時はerrMsgを空文字で呼ぶ。
	UpdateStatus func(tx *gorm.DB, status string, errMsg string) error
	// OnSuccess は変換が成功しSavePathへの保存が完了した後にだけ呼ばれる。
	// (例: GPU解析キューへの投入。まだ出来上がっていない画像を解析に回さないため)
	OnSuccess func()
}

// scheduleFallbackConversion は、フロントエンド(createImageBitmapのネイティブデコード/heic2anyの
// どちらも)で変換できなかったHEIC/RAWファイルを、heif-convert/exiftoolでバックグラウンド変換する。
// 呼び出し側はこの完了を待たない(先にDBレコードをProcessing状態で作成し、HTTPレスポンスは即座に返す)。
// 同時実行数は acquireFallbackSlot (heic_raw_fallback.go)で既に絞られているため、
// ここでgoroutineをいくつ積んでも外部プロセス(heif-convert/exiftool)が際限なく並行起動することはない。
func scheduleFallbackConversion(job fallbackJob) {
	go func() {
		img, err := decodeWithFallback(job.OriginalPath, job.OriginalFilename)
		if err != nil {
			log.Printf("[FALLBACK-WORKER-ERROR] ❌ 変換失敗 (file: %s): %v", job.OriginalFilename, err)
			if updErr := job.UpdateStatus(job.Database, model.ConversionStatusFailed, err.Error()); updErr != nil {
				log.Printf("[FALLBACK-WORKER-ERROR] ❌ ステータス更新失敗 (file: %s): %v", job.OriginalFilename, updErr)
			}
			return
		}

		img = resizeToMaxLongSide(img)
		if err := imaging.Save(img, job.SavePath, imaging.JPEGQuality(85)); err != nil {
			log.Printf("[FALLBACK-WORKER-ERROR] ❌ リサイズ画像の保存に失敗 (file: %s): %v", job.OriginalFilename, err)
			if updErr := job.UpdateStatus(job.Database, model.ConversionStatusFailed, err.Error()); updErr != nil {
				log.Printf("[FALLBACK-WORKER-ERROR] ❌ ステータス更新失敗 (file: %s): %v", job.OriginalFilename, updErr)
			}
			return
		}

		if err := job.UpdateStatus(job.Database, model.ConversionStatusReady, ""); err != nil {
			log.Printf("[FALLBACK-WORKER-ERROR] ❌ ステータス更新失敗 (file: %s): %v", job.OriginalFilename, err)
			return
		}

		log.Printf("[FALLBACK-WORKER] ✅ バックグラウンド変換が完了しました (file: %s -> %s)", job.OriginalFilename, job.SavePath)
		if job.OnSuccess != nil {
			job.OnSuccess()
		}
	}()
}
