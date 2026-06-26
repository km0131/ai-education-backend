package worker

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/goccy/go-json"
)

// MetadataEntry は、JSONに含める各画像の情報を定義します
type MetadataEntry struct {
	Filename string `json:"filename"`
	LabelID  int    `json:"label_id"`
}

// CreateTrainingZip は収集したデータから metadata.json と画像群を含むZIPファイルを一時ディレクトリに作成します
func CreateTrainingZip(trainingData map[int][]string, jobID uint) (string, error) {
	// 一時ディレクトリ（/tmp など）にZIPファイルを作成
	zipFilename := fmt.Sprintf("training_job_%d.zip", jobID)
	zipPath := filepath.Join(os.TempDir(), zipFilename)

	zipFile, err := os.Create(zipPath)
	if err != nil {
		return "", fmt.Errorf("failed to create temp zip file: %w", err)
	}
	// エラーが起きた場合は開いたファイルを閉じる（正常時は最後に明示的にクローズします）
	defer zipFile.Close()

	// zip.Writer を初期化
	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	var metadataList []MetadataEntry
	imageCounter := 0

	// 2. 各ラベルの画像をループしてZIPに書き込む
	for labelID, paths := range trainingData {
		for _, srcPath := range paths {
			// 元ファイル（ラズパイ上の画像）を開く
			srcFile, err := os.Open(srcPath)
			if err != nil {
				log.Printf("[WARN] 画像ファイルが開けません(スキップします): %s, err: %v", srcPath, err)
				continue
			}

			// ZIP内でのバグを防ぐため、元のファイル名ではなく連番で安全なファイル名を生成
			// 例: srcPath の拡張子（.jpgなど）を維持しつつ "photo_0.jpg" にする
			ext := filepath.Ext(srcPath)
			zipInnerFilename := fmt.Sprintf("photo_%d%s", imageCounter, ext)
			imageCounter++

			// ZIP内に新しいファイルのエントリを作成
			writer, err := zipWriter.Create(zipInnerFilename)
			if err != nil {
				srcFile.Close()
				return "", fmt.Errorf("failed to create zip entry for %s: %w", zipInnerFilename, err)
			}

			// データをコピー（メモリを圧迫しないストリームコピー）
			if _, err := io.Copy(writer, srcFile); err != nil {
				srcFile.Close()
				return "", fmt.Errorf("failed to write file to zip: %w", err)
			}
			srcFile.Close()

			// メタデータ（対応表）の配列に追加
			metadataList = append(metadataList, MetadataEntry{
				Filename: zipInnerFilename,
				LabelID:  labelID,
			})
		}
	}

	// 3. metadata.json を作成してZIPに同梱する
	jsonBytes, err := json.MarshalIndent(metadataList, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal metadata to json: %w", err)
	}

	jsonWriter, err := zipWriter.Create("metadata.json")
	if err != nil {
		return "", fmt.Errorf("failed to create zip entry for metadata.json: %w", err)
	}

	if _, err := jsonWriter.Write(jsonBytes); err != nil {
		return "", fmt.Errorf("failed to write metadata.json to zip: %w", err)
	}

	// deferに頼らず、ここで明示的にクローズして確実にディスクに書き込みを完了させる
	if err := zipWriter.Close(); err != nil {
		return "", fmt.Errorf("failed to close zip writer: %w", err)
	}
	if err := zipFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close zip file: %w", err)
	}

	log.Printf("[INFO] ZIPファイルの作成が完了しました: %s (画像数: %d 枚)", zipPath, imageCounter)
	return zipPath, nil
}
