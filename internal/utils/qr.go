package utils

import (
	"encoding/base64"
	"fmt"
	"github.com/skip2/go-qrcode" // 使用しているライブラリに合わせて
)

// GetQRCode は ID とパスワードから Base64 形式の QR コードを作成します
func GetQRCode(id string, password string) (string, error) {
	qrContent := fmt.Sprintf("$ID=%s$pass=%s", id, password)

	// Panic ではなくエラーを返却するように修正
	png, err := qrcode.Encode(qrContent, qrcode.Medium, 256)
	if err != nil {
		return "", fmt.Errorf("QR コードの生成に失敗しました: %w", err)
	}

	return base64.StdEncoding.EncodeToString(png), nil
}
