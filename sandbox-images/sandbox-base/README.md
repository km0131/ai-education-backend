# sandbox-base

生徒用サンドボックスコンテナのベースイメージ。

## 含まれるもの
- Ubuntu 24.04
- Python 3(python3 / python3-pip / python3-venv)
- git
- pyright(Python用言語サーバー、LSP補完用)
- 非rootユーザー `student`(コンテナ起動時のデフォルトユーザー、ホームディレクトリ `/home/student`)

## ビルド方法
```bash
cd sandbox-images/sandbox-base
./build.sh
```
`sandbox-base:latest` と `sandbox-base:YYYYMMDD`(ビルド日付タグ)の2つのタグが作成される。

## 更新時の手順
1. `Dockerfile` を編集
2. `./build.sh` を再実行
3. `docker run --runtime=runsc --rm sandbox-base:latest python3 --version` 等で動作確認
4. 生徒環境に反映する場合は、コンテナ管理側の参照イメージタグを更新
