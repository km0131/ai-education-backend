package handler

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	DB *gorm.DB
}

func (h *Handler) respondError(c *gin.Context, status int, action, message string, err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
		log.Printf("[%s] %s: %v", action, message, err)
	} else {
		log.Printf("[%s] %s", action, message)
	}

	if h.DB != nil {
		if saveErr := db.SaveSystemLog(h.DB, "ERROR", action, message, detail, nil); saveErr != nil {
			log.Printf("[エラーログ保存失敗] %v", saveErr)
		}
	}

	c.JSON(status, gin.H{"error": message})
}

// PostLogin はログイン認証を処理し、ユーザーが入力したユーザー名から
// パスワード画像リストを返します。
func (h *Handler) PostLogin(c *gin.Context) {
	var req struct {
		InputUsername string `json:"inputUsername" binding:"required"`
	}

	if err := c.BindJSON(&req); err != nil {
		h.respondError(c, http.StatusBadRequest, "ログイン認証", "リクエストの形式が不正です", err)
		return
	}

	fetchedUser, err := db.FindUserByName(h.DB, req.InputUsername)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.respondError(c, http.StatusUnauthorized, "ログイン認証", "ユーザーが見つかりませんでした", err)
			return
		}
		h.respondError(c, http.StatusInternalServerError, "ログイン認証", "ユーザー情報の取得に失敗しました", err)
		return
	}

	// PasswordGroup をパース（カンマ区切りの数字を[]intに変換）
	var numbers []int
	stringValues := strings.Split(fetchedUser.PasswordGroup, ",")
	for _, s := range stringValues {
		s = strings.TrimSpace(s)
		if num, err := strconv.Atoi(s); err == nil {
			numbers = append(numbers, num)
		}
	}

	// ユーザーのパスワード画像を取得
	list, name, err := db.Image_DB(h.DB, numbers)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "ログイン認証", "画像情報の取得に失敗しました", err)
		return
	}
	log.Printf("[ログイン認証] 画像情報の取得に成功しました: %v", list)

	c.JSON(http.StatusOK, gin.H{
		"status":    "next_step",
		"user_id":   fetchedUser.ID.String(),
		"user_name": fetchedUser.Name,
		"user_teacher": func() string {
			if fetchedUser.Teacher {
				return "teacher"
			}
			return "student"
		}(),
		"img_list":   list,
		"img_name":   name,
		"img_number": numbers,
	})
}

// GetSignup は新規登録用の画像リストを返します。
func (h *Handler) GetSignup(c *gin.Context) {
	list, name, number, err := db.Random_image(h.DB)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "会員登録", "登録用画像の生成に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "会員登録用画像です",
		"img_list":   list,
		"img_name":   name,
		"img_number": number,
	})
}

// PostSignup は新規登録を処理します。
// フロントエンドから { username, role, images, email } を受け取り、ユーザーを作成します。
func (h *Handler) PostSignup(c *gin.Context) {
	var req model.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.respondError(c, http.StatusBadRequest, "会員登録", "リクエストの形式が不正です", err)
		return
	}

	// 画像番号スライスを保存用文字列に変換 (例: "1,2,3")
	numStr := serializeIntSlice(req.ImagesOriginal)
	log.Printf("ユーザー登録リクエスト: username=%s, role=%s, original_images=%v, email=%s", req.Username, req.Role, req.ImagesOriginal, req.Email)

	// ロール判定
	isTeacher := req.Role == "teacher"
	email := req.Email
	if !isTeacher || email == "" {
		email = "null" // 生徒、またはメール未入力時
	}

	// パスワード(画像ラベル)の処理
	if len(req.Images) < 3 {
		h.respondError(c, http.StatusBadRequest, "会員登録", "パスワード画像が不足しています", nil)
		return
	}
	// 既存の処理
	rawPassword := serializeIntSlice(req.Images[:3])
	combinedName, err := h.PostLoginByImages(rawPassword)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "会員登録", "画像情報の取得に失敗しました", err)
		return
	}

	// セキュリティ処理（ハッシュ化・トークン生成）
	// Argon2などでハッシュ化
	hashedPassword, err := utils.HashPasswordWithDefault(combinedName)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "会員登録", "パスワードのハッシュ化に失敗しました", err)
		return
	}

	qrToken := utils.GenerateRandomToken()
	hashedQRToken, _ := utils.HashPasswordWithDefault(qrToken)

	// 5. DB保存
	userID, Name, err := db.InsertUser(h.DB, req.Username, hashedPassword, numStr, email, isTeacher, hashedQRToken)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "会員登録", "ユーザーの保存に失敗しました", err)
		return
	}

	// 6. QRコード生成
	qrCode, err := utils.GetQRCode(userID, qrToken)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "会員登録", "QRコードの生成に失敗しました", err)
		return
	}

	// レスポンス送信
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "ユーザー登録が完了しました",
		"QR":      qrCode,
		"ID":      Name,
		"name":    req.Username,
		"teacher": isTeacher,
	})
}

// PostLoginRegistrer は画像パスワード照合ハンドラーです。
func (h *Handler) PostLoginRegistrer(c *gin.Context) {
	var req model.LoginRequest
	if err := c.BindJSON(&req); err != nil {
		h.respondError(c, http.StatusBadRequest, "画像照合", "リクエストの形式が不正です", err)
		return
	}

	fetchedUser, err := db.FindUserByName(h.DB, req.Username)
	if err != nil {
		h.respondError(c, http.StatusUnauthorized, "画像照合", "ユーザーが見つかりませんでした", err)
		return
	}

	// パスワード(画像ラベル)の処理
	if len(req.Images) < 3 {
		h.respondError(c, http.StatusBadRequest, "画像照合", "パスワード画像が不足しています", nil)
		return
	}

	// パスワード画像を連結
	password1 := serializeIntSlice(req.Images[:3])
	combinedName, err := h.PostLoginByImages(password1)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "画像照合", "画像情報の取得に失敗しました", err)
		return
	}

	match, err := utils.VerifyPassword(combinedName, fetchedUser.Password)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "画像照合", "パスワードの確認に失敗しました", err)
		return
	}

	// DBから取得したパスワードと照合
	if match {
		c.JSON(http.StatusOK, gin.H{
			"password": true,
		})
	} else {
		c.JSON(http.StatusOK, gin.H{
			"password": false,
			"error":    "パスワードが一致しません",
		})
	}
}

// PostLoginQR はQRコードログインハンドラーです。
func (h *Handler) PostLoginQR(c *gin.Context) {
	var req struct {
		QRData string `json:"qr_data" binding:"required"`
	}

	if err := c.BindJSON(&req); err != nil {
		h.respondError(c, http.StatusBadRequest, "QRログイン", "リクエストの形式が不正です", err)
		return
	}

	// 簡易実装: QRコードからユーザーIDを抽出
	// 実際の実装では復号化と照合が必要
	if req.QRData == "" {
		h.respondError(c, http.StatusUnauthorized, "QRログイン", "無効なQRデータです", nil)
		return
	}

	// ここでは簡易実装として、QRコードが有効と仮定
	c.JSON(http.StatusOK, gin.H{
		"status":   "success",
		"password": true,
	})
}

// serializeIntSlice はintのスライスをカンマ区切りの文字列に変換します。
func serializeIntSlice(slice []int) string {
	sb := strings.Builder{}
	for i, v := range slice {
		sb.WriteString(strconv.Itoa(v))
		if i < len(slice)-1 {
			sb.WriteString(",")
		}
	}
	return sb.String()
}

// パスワード画像の提供ハンドラー
func (h *Handler) PostPasswordImage(c *gin.Context) {
	// 【修正】「ilename :=」を「filename :=」に正しく直しました
	filename := c.Param("filename")

	// 1. Ginの仕様上、先頭に「/」が入るため、それを取り除く
	filename = strings.TrimPrefix(filename, "/")

	// 2. セキュリティ対策: filepath.Clean で「../」などを排除
	cleanedPath := filepath.Clean(filename)

	// 「../」を使って親ディレクトリに遡ろうとする攻撃だけをブロックする
	if strings.HasPrefix(cleanedPath, "..") {
		h.respondError(c, 403, "ファイル取得", "不正なファイルアクセスを検知しました", nil)
		return
	}

	// 3. コンテナ内の絶対パスをベースにする
	basePath := "/app/images/certification"
	fullPath := filepath.Join(basePath, cleanedPath)

	log.Printf("[DEBUG] 検索中の絶対パス: %s", fullPath)

	// 4. ファイルの実在チェック
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		h.respondError(c, 404, "ファイル取得", "ファイルが見つかりませんでした", err)
		return
	}

	// ファイルを返却
	c.File(fullPath)
}

func (h *Handler) PostLoginByImages(rawPassword string) (string, error) {
	idStrings := strings.Split(rawPassword, ",")
	// 文字列のスライスを整数のスライスに変換
	var ids []int
	for _, s := range idStrings {
		if id, err := strconv.Atoi(s); err == nil {
			ids = append(ids, id)
		}
	}
	nameList, err := db.GetImageNamesByIDs(h.DB, ids)
	if err != nil {
		// エラーハンドリング
		return "", err
	}

	//名前を連結する
	combinedName := strings.Join(nameList, "")

	return combinedName, nil
}
