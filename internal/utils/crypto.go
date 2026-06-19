package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
)

// Encrypt は文字列を暗号化し、Base64形式で返します
func Encrypt(plaintext string) (string, error) {
	// 環境変数から32バイトの鍵を取得
	keyStr := os.Getenv("APP_MASTER_KEY")
	// opensslで生成したBase64形式をデコードして32バイトのバイナリにする
	key, err := base64.StdEncoding.DecodeString(keyStr)
	if err != nil || len(key) != 32 {
		return "", errors.New("マスターキーが不正です。Base64 で 32 バイトにしてください")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// ナンス（12バイト）をランダム生成
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	// 暗号化実行。ナンスを先頭にくっつけて返す（gcm.Sealの第1引数がプレフィックスになる）
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt はBase64形式の暗号文を元の文字列に戻します
func Decrypt(cryptoText string) (string, error) {
	keyStr := os.Getenv("APP_MASTER_KEY")
	key, err := base64.StdEncoding.DecodeString(keyStr)
	if err != nil || len(key) != 32 {
		return "", errors.New("マスターキーが不正です")
	}

	data, err := base64.StdEncoding.DecodeString(cryptoText)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("暗号文が短すぎます")
	}

	// 先頭12バイト(ナンス)とそれ以降(暗号文)に分ける
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err // 改ざん検知機能。鍵が違うかデータが壊れているとここでエラーになる
	}

	return string(plaintext), nil
}
