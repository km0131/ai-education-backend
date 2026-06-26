package utils

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"aidanwoods.dev/go-paseto"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var (
	symmetricKey  paseto.V4SymmetricKey
	tokenLifetime time.Duration
)

func init() {

	keyStr := os.Getenv("PASETO_KEY")
	if keyStr == "" {
		panic("PASETO_KEY が設定されていません")
	}

	var err error
	symmetricKey, err = paseto.V4SymmetricKeyFromHex(keyStr)
	if err != nil {
		panic(fmt.Sprintf("不正なPASETO_KEY: %v", err))
	}

	expHoursStr := os.Getenv("PASETO_EXPIRATION_HOURS")
	if expHoursStr == "" {
		expHoursStr = "24"
	}

	expHours, err := strconv.Atoi(expHoursStr)
	if err != nil {
		panic("PASETO_EXPIRATION_HOURS は整数で指定してください")
	}

	tokenLifetime = time.Duration(expHours) * time.Hour
}

// CustomClaims はトークンに埋め込む独自データです
type CustomClaims struct {
	UserID uuid.UUID `json:"user_id"`
}

// GeneratePasetoToken は PASETO v4.Local トークンを生成します（暗号化＋改ざん検知）
func GeneratePasetoToken(userID uuid.UUID) (string, error) {
	now := time.Now()

	// 1. 標準の暗号化クレーム（有効期限など）を設定
	token := paseto.NewToken()
	token.SetIssuedAt(now)
	token.SetNotBefore(now)
	token.SetExpiration(now.Add(tokenLifetime))

	// 2. 独自のユーザー情報を埋め込む
	err := token.Set("user_id", userID.String())
	if err != nil {
		return "", err
	}

	// 3. V4Local（共通鍵暗号）方式で暗号化してトークン文字列を生成
	// これにより、第三者には中身を一切解読できない文字列（v4.local.〜）になります
	tokenString := token.V4Encrypt(symmetricKey, nil)

	return tokenString, nil
}

// VerifyPasetoToken はトークンを復号・検証し、ユーザー情報を返します
func VerifyPasetoToken(tokenString string) (*CustomClaims, error) {
	// v4用のパーサー（検証器）を作成
	parser := paseto.NewParser()

	// トークンを復号・検証
	token, err := parser.ParseV4Local(symmetricKey, tokenString, nil)
	if err != nil {
		return nil, errors.New("トークンが不正、または有効期限が切れています")
	}

	// データの取り出し
	var userIDStr string

	if err := token.Get("user_id", &userIDStr); err != nil {
		return nil, errors.New("トークン内のユーザーIDが不正です")
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return nil, errors.New("UUID形式が不正です")
	}

	return &CustomClaims{
		UserID: userID,
	}, nil
}

// MachineToMachineAuth ミドルウェア：Python用
func MachineToMachineAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 環境変数からM2M専用の合言葉を取得（PASETOの鍵とは別のものを推奨）
		secretToken := os.Getenv("CALLBACK_SECRET")
		if secretToken == "" {
			// 未設定時のフォールバック（本番では必ず環境変数に設定してください）
			panic("PASETO_KEY が設定されていません")
		}

		authHeader := c.GetHeader("Authorization")
		expectedHeader := "Bearer " + secretToken

		if authHeader != expectedHeader {
			c.JSON(401, gin.H{"error": "Unauthorized: システム間認証トークンが一致しません"})
			c.Abort()
			return
		}

		c.Next()
	}
}
