package handler

import (
	"net/http"

	"ai-education/backend/internal/db"

	"github.com/gin-gonic/gin"
)

// ListFeatureTypes: クラス作成フォームが選択肢を動的に取得するための一覧API。
// フロント側で選択肢をハードコードしないため(image_classification/web_dev以外の
// 追加も、このAPIとfeature_typesテーブルへの行追加だけで完結させる)。
func (h *Handler) ListFeatureTypes(c *gin.Context) {
	types, err := db.ListFeatureTypes(h.DB)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "機能種別一覧取得", "機能種別一覧の取得に失敗しました", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"feature_types": types})
}
