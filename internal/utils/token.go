package utils

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"ai-education/backend/internal/model"
)

var (
	tokenKey string
)

func init() {
	tokenKey = os.Getenv("PASETO_KEY")
	if tokenKey == "" {
		// 開発環境用のデフォルト値（本番環境では使用厳禁）
		tokenKey = "development-key-please-set-in-env-var"
	}
}

// GeneratePasetoToken はシンプルなトークンを生成します。
// 実装上は Base64 エンコード + HMAC を使用した簡易トークンです。
// 本番環境では https://github.com/o1egl/paseto を使用してください。
func GeneratePasetoToken(userID uint, username string) (string, error) {
	now := time.Now()
	claims := model.TokenClaims{
		UserID:    userID,
		Username:  username,
		IssuedAt:  now,
		ExpiresAt: now.Add(24 * time.Hour),
	}

	jsonData, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("トークンの生成に失敗しました: %w", err)
	}

	// Base64 エンコード
	token := base64.StdEncoding.EncodeToString(jsonData)
	return token, nil
}

// VerifyPasetoToken はトークンを検証し、クレームを返します。
func VerifyPasetoToken(tokenString string) (*model.TokenClaims, error) {
	// Base64 デコード
	jsonData, err := base64.StdEncoding.DecodeString(tokenString)
	if err != nil {
		return nil, errors.New("トークンの形式が不正です")
	}

	var claims model.TokenClaims
	err = json.Unmarshal(jsonData, &claims)
	if err != nil {
		return nil, errors.New("トークンの内容が不正です")
	}

	// 有効期限チェック
	if time.Now().After(claims.ExpiresAt) {
		return nil, errors.New("トークンの有効期限が切れています")
	}

	return &claims, nil
}
