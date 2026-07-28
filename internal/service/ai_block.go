package service

import (
	"ai-education/backend/internal/db"
	"errors"

	"gorm.io/gorm"
)

// ErrAiCreationBlocked は、先生がクラス単位でAIの新規作成/学習開始/性能テストを一時停止している
// 場合に返るセンチネルエラー。ハンドラー側でerrors.Isにより判定し、423 Lockedを返す。
var ErrAiCreationBlocked = errors.New("このクラスは現在AIの作成・学習・テストが停止されています")

// checkAiCreationNotBlocked は、AIを新しく作成/学習開始/性能テストの各処理の先頭で呼び出す
// 最終防衛ラインのチェック。フロント側でもモーダルを開く直前に事前確認するが、
// 直接API呼び出しで回避される可能性があるため、バックエンド側でも必ず確認する。
func checkAiCreationNotBlocked(database *gorm.DB, courseID uint) error {
	blocked, err := db.IsAiCreationBlocked(database, courseID)
	if err != nil {
		return err
	}
	if blocked {
		return ErrAiCreationBlocked
	}
	return nil
}
