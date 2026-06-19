package db

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"path"
	"strconv"

	"ai-education/backend/internal/model"
	"gorm.io/gorm"
)

// 画像番号からリンクとDB検索を行う
func Image_DB(db *gorm.DB, number []int) ([]string, []string, error) {
	var fetchedCertifications []model.Certification
	result := db.Where("id IN ?", number).Find(&fetchedCertifications) //１回で全てのデータを取得
	if result.Error != nil {
		// IDが無い
		return nil, nil, fmt.Errorf("指定IDリストのデータ取得に失敗しました: %w", result.Error)
	}
	const selectionCount = 10
	list := make([]string, 0, selectionCount)
	name := make([]string, 0, selectionCount)

	// 定数ではなく実際の画像保存ディレクトリを指定
	const baseDir = "images/certification"

	for _, i := range fetchedCertifications {
		imageIDStr := strconv.Itoa(int(i.ID))
		r := path.Join(baseDir, imageIDStr+".png") // 画像リンクの作成
		list = append(list, r)                     //画像のパスをリストに追加
		name = append(name, i.Name)                // 画像の名前をリストに追加
	}
	// 取得した数が必要な数と異なるときのチェック
	if len(fetchedCertifications) != len(number) {
		// ランダムに選ばれたIDの一部が見つからなかった
		log.Printf("警告: 期待値 %d 件に対し、取得件数は %d 件でした。", len(number), len(fetchedCertifications))
	}
	return list, name, nil
}

// ランダムに画像を選択
func Random_image(db *gorm.DB) ([]string, []string, []int, error) {
	const totalCount = 30
	const selectCount = 12

	// 1. 1から30までのリストを作成
	candidates := make([]int, totalCount)
	for i := 0; i < totalCount; i++ {
		candidates[i] = i + 1
	}

	// 2. crypto/rand を使ったフィッシャー・イェーツ・シャッフル
	for i := len(candidates) - 1; i > 0; i-- {
		// 0 から i までのランダムなインデックスを選択
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return nil, nil, nil, err
		}
		j := int(n.Int64())
		// 要素を入れ替える
		candidates[i], candidates[j] = candidates[j], candidates[i]
	}

	// 3. 先頭の12個を取り出す
	number := candidates[:selectCount]

	// 4. 画像リンクと名前を取得
	list, name, err := Image_DB(db, number)
	if err != nil {
		// log.Fatalを使うとサーバーが止まるので、実運用ではエラーを返すのが一般的です
		return nil, nil, nil, fmt.Errorf("画像リストのDB検索エラー: %w", err)
	}
	log.Printf("DEBUG: Image_DB success. Name: %v, ListSize: %d", name, len(list))

	return list, name, number, nil
}

func GetImageNamesByIDs(tx *gorm.DB, ids []int) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}

	var certifications []model.Certification

	// IN句を使って、渡されたIDのデータを一括で取得します
	result := tx.Where("id IN ?", ids).Find(&certifications)
	if result.Error != nil {
		return nil, fmt.Errorf("画像名の取得に失敗しました: %w", result.Error)
	}

	// 取得したデータから名前（Name）だけを抜き出して配列にする
	names := make([]string, 0, len(certifications))
	for _, item := range certifications {
		names = append(names, item.Name)
	}

	return names, nil
}
