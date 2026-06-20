package handler

import (
	"fmt"
	"io"

	"github.com/gin-gonic/gin"
)

func (h *Handler) CreateClass(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(400, gin.H{"error": "読み込み失敗"})
		return
	}
	// 2. ターミナルに文字列として出力して確認！
	fmt.Println("--- 届いた生データ ---")
	fmt.Println(string(bodyBytes))
	fmt.Println("--------------------")
	//userIDCtx, exists := c.Get("userID")
	//if !exists {
	//	c.JSON(http.StatusUnauthorized, gin.H{"error": "認証されていません"})
	//	return
	//}
	//userID, _ := userIDCtx.(uint)
	//
	//role, err := db.TeacherCheck(h.DB, userID)
	//if err != nil {
	//	c.JSON(http.StatusForbidden, gin.H{"error": "先生しかクラスを作成できません"})
	//	return
	//}
	//
	//var input model.CreateClassInput
	//if err := c.ShouldBindJSON(&input); err != nil {
	//	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	//	return
	//}
	//
	//inviteCode, err := db.GenerateRandomSuffix(6)
	//if err != nil {
	//	c.JSON(http.StatusInternalServerError, gin.H{"error": "招待コードの生成に失敗しました"})
	//	return
	//}
	//
	//code, err := db.ClassCreation()
}
