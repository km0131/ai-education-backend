package model

import "time"

// RegisterRequest はユーザー登録APIのリクエストボディです。
type RegisterRequest struct {
	Username       string `json:"username" binding:"required"`
	Role           string `json:"role" binding:"required"`
	Images         []int  `json:"images" binding:"required"`
	ImagesOriginal []int  `json:"image_original" binding:"required"`
	Email          string `json:"email"`
}

// TokenClaims は Paseto トークンのペイロード構造です。
type TokenClaims struct {
	UserID       uint      `json:"user_id"`
	Username     string    `json:"username"`
	ImageNumbers []int     `json:"image_numbers,omitempty"`
	IssuedAt     time.Time `json:"iat"`
	ExpiresAt    time.Time `json:"exp"`
}

// LoginRequest はログインAPIのリクエストボディです。
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Images   []int  `json:"images" binding:"required"`
}

// フロントエンドから送られてくるJSONをマッピングする構造体
type CreateClassInput struct {
	ClassName   string `json:"className" binding:"required"`
	Description string `json:"description"`
}
