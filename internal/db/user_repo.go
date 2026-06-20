package db

import (
	"ai-education/backend/internal/model"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/width"
)

// InsertUser は新しいユーザーをデータベースに挿入します。
func InsertUser(db *gorm.DB, username, hashPassword, passwordGroup, email string, teacher bool, qrPassword string) (string, string, error) {
	desiredName := username // ユーザーが希望する名前をそのまま使用（ただし、後で一意性を保つためにサフィックスを追加）
	const maxRetries = 5    // ユーザー名の重複が発生した場合の最大リトライ回数(5回)
	for i := 0; i < maxRetries; i++ {
		finalUsername, err := createUniqueUsername(desiredName)
		if err != nil {
			return "", "", err
		}
		user := model.User{
			ID:            uuid.New(),
			Name:          finalUsername,
			Password:      hashPassword,
			PasswordGroup: passwordGroup,
			Email:         email,
			Teacher:       teacher,
			QRpassword:    qrPassword,
		}
		// とりあえずDBに保存してみる
		err = db.Create(&user).Error
		if err == nil {
			// 成功した場合は、その場でIDを返して関数を終了！
			return user.ID.String(), finalUsername, nil
		}
		// エラーが「ユニーク制約（重複）エラー」かどうかをチェック
		errStr := strings.ToLower(err.Error())
		isUniqueError := strings.Contains(errStr, "unique") || strings.Contains(errStr, "duplicate")
		if isUniqueError {
			// 重複していたら、ログを出して次のループ（リトライ）へ
			continue
		}
		// 重複以外の深刻なエラー（DB切断など）は即リターン
		return "", "", fmt.Errorf("ユーザーの保存に失敗しました: %w", err)
	}
	// 5回リトライしてもすべて重複エラーで全滅した場合
	return "", "", fmt.Errorf("一意なユーザー名を %d 回試行しても生成できませんでした", maxRetries)
}

// FindUserByName はユーザー名を元にユーザーを検索します。
func FindUserByName(db *gorm.DB, username string) (model.User, error) {
	var user model.User
	result := db.Where("name = ?", username).First(&user)
	return user, result.Error
}

// FindUserByID はIDを元にユーザーを検索します。
func FindUserByID(db *gorm.DB, userid string) (model.User, error) {
	var user model.User
	result := db.Where("id = ?", userid).First(&user)
	return user, result.Error
}

// 名前の一意性を保つために4桁のUUIDを生成し追加
func createUniqueUsername(desiredName string) (string, error) {
	// ユーザー名の長さ制限に合わせて、希望の名前を正規化/短縮する
	normalizedName, err := normalizeJapaneseUsername(desiredName)
	if err != nil {
		return "", fmt.Errorf("ユーザー名の正規化に失敗しました: %w", err)
	}
	// ４文字のランダムなサフィックスを生成
	suffix, err := GenerateRandomSuffix(4)
	if err != nil {
		return "", fmt.Errorf("ユーザー名のサフィックス生成に失敗しました: %w", err)
	}
	// 結合して最終的なユーザー名を生成 (例: tanaka-h7v8xPzM)
	finalUsername := fmt.Sprintf("%s-%s", normalizedName, suffix)
	log.Printf("生成されたユーザー名: %s", finalUsername)
	// DBのUNIQUE制約と競合した場合は、呼び出し元でこの関数を再試行するロジックが必要
	return finalUsername, nil
}

// 一意性を保つための複合正規化処理
func normalizeJapaneseUsername(desiredName string) (string, error) {
	// 濁音とかを処理して半角に
	t := transform.Chain(norm.NFKC, width.Fold)
	output, _, err := transform.String(t, desiredName)
	if err != nil {
		return "", err
	}
	// 英字部分を小文字に統一する
	normalizedName := strings.ToLower(output)
	// 空白や制御文字の除去 (必要に応じて)
	normalizedName = strings.TrimSpace(normalizedName)
	return normalizedName, nil
}

// UUIDの生成名前用
func GenerateRandomSuffix(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, length)

	for i := 0; i < length; i++ {
		// charsetの長さに基づいた安全な乱数を生成
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", fmt.Errorf("乱数の生成に失敗しました: %w", err)
		}
		result[i] = charset[num.Int64()]
	}

	return string(result), nil
}

// 先生チェック
func TeacherCheck(db *gorm.DB, userID uint) (string, uuid.UUID, error) {
	// ユーザ情報取得
	user, err := FindUserByName(db, strconv.Itoa(int(userID)))
	if err != nil {
		return "", uuid.Nil, err
	}
	// 先生チェック
	if user.Teacher == true {
		return "", uuid.Nil, errors.New("ユーザーは先生ではありません")
	}
	// 先生だった場合は、そのロール名を返す
	return strconv.Itoa(int(userID)), user.ID, nil
}
