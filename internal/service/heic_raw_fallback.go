package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// フォールバック変換1回あたりの上限時間。ラズパイ上で外部コマンドが固まった場合に
// バックグラウンドジョブが無限に詰まらないようにする。この処理は非同期ジョブ
// (service.scheduleFallbackConversion)からのみ呼ばれ、HTTPリクエストをブロックしないため、
// 30秒よりやや余裕を持たせて10bit HDR等の重いデコードにも耐えられるようにしている。
const fallbackConvertTimeout = 45 * time.Second

// フォールバック変換(heif-convert/exiftool)の同時実行数の上限。
// 複数の変換が同時に走ると外部プロセスが並行起動してラズパイのCPU/メモリを圧迫するため、
// トークンプール(バッファ付きチャネル)で頭数を2〜3件程度に絞る。
const maxConcurrentFallbackConversions = 2

// フォールバック変換の順番待ちに使う上限時間。空きスロットが出ないまま
// これを超えた場合は、混雑中としてエラーを返す。この待ちはバックグラウンドジョブの中で
// 発生するためHTTPリクエストはブロックしない。フロント側の対応でこの経路に来るファイル数
// 自体が減る見込みだが、大量アップロード(最大500枚/人)時にキューが詰まっても粘れるよう、
// 同期実行だった頃の60秒より長めに取っている。
const fallbackQueueWaitTimeout = 5 * time.Minute

var fallbackConvertSlots = make(chan struct{}, maxConcurrentFallbackConversions)

// acquireFallbackSlot は同時実行数の上限に空きが出るまで待機する。
// 空きが出れば解放用の関数を返し、fallbackQueueWaitTimeoutを超えて空きが
// 出ない場合はエラーを返す。
func acquireFallbackSlot() (func(), error) {
	select {
	case fallbackConvertSlots <- struct{}{}:
		return func() { <-fallbackConvertSlots }, nil
	case <-time.After(fallbackQueueWaitTimeout):
		return nil, fmt.Errorf("フォールバック変換が混み合っています。しばらくしてから再試行してください")
	}
}

func isHeicFilename(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".heic" || ext == ".heif"
}

func isRawFilename(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".cr2" || ext == ".cr3"
}

// needsFallbackConversion は、imaging.Openでの通常デコードが失敗した画像について、
// heif-convert/exiftoolによる非同期フォールバック変換の対象になり得るかを判定する。
// それ以外の拡張子(本当に壊れている/未対応の形式)は即エラーにする。
func needsFallbackConversion(originalFilename string) bool {
	return isHeicFilename(originalFilename) || isRawFilename(originalFilename)
}

// convertHeicToJpeg は、フロントエンドで変換できなかったHEIC/HEIFファイルを
// libheif-tools の heif-convert コマンドでJPEGに変換する。
// Apple純正のHEICエンコード方式(新しいiPhoneの10bit HDR等)を含め互換性が高く、
// Goバイナリ自体にcgoリンクを持ち込まずに済むため、フォールバック用途として採用している。
func convertHeicToJpeg(srcPath string) (string, error) {
	release, err := acquireFallbackSlot()
	if err != nil {
		return "", err
	}
	defer release()

	dstPath := srcPath + ".fallback.jpg"

	ctx, cancel := context.WithTimeout(context.Background(), fallbackConvertTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "heif-convert", "-q", "85", srcPath, dstPath)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("heif-convert failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	if info, err := os.Stat(dstPath); err != nil || info.Size() == 0 {
		os.Remove(dstPath)
		return "", fmt.Errorf("heif-convert did not produce a usable output file")
	}
	return dstPath, nil
}

// extractRawPreviewJpeg は、一眼カメラのRAWファイル(CR2/CR3)に埋め込まれているJPEGプレビューを
// exiftool で抽出する。学習データとして必要なのは512px程度のサムネイルであり、フルRAWデモザイクは
// ラズパイには重すぎるため、軽量なプレビュー抽出のみをフォールバックとして行う。
func extractRawPreviewJpeg(srcPath string) (string, error) {
	release, err := acquireFallbackSlot()
	if err != nil {
		return "", err
	}
	defer release()

	dstPath := srcPath + ".fallback.jpg"

	ctx, cancel := context.WithTimeout(context.Background(), fallbackConvertTimeout)
	defer cancel()

	out, err := os.Create(dstPath)
	if err != nil {
		return "", fmt.Errorf("failed to create preview output file: %w", err)
	}
	defer out.Close()

	cmd := exec.CommandContext(ctx, "exiftool", "-b", "-PreviewImage", srcPath)
	cmd.Stdout = out
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		os.Remove(dstPath)
		return "", fmt.Errorf("exiftool failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	info, err := os.Stat(dstPath)
	if err != nil || info.Size() == 0 {
		os.Remove(dstPath)
		return "", fmt.Errorf("RAWファイルにプレビュー画像が見つかりませんでした")
	}
	return dstPath, nil
}
