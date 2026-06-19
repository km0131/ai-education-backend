# 安定版のイメージを指定（ラズパイOSが64bitなら aarch64 が自動で選ばれます）
FROM docker.io/library/golang:1.25-alpine

# ビルドに必要な最小限のツール（gccなど）をインストール
# GORMなどでCGOを使う場合は build-base が必要になることがあります
RUN apk add --no-cache git gcc musl-dev

WORKDIR /app

# ホットリロードツール Air のインストール
# air-verse に移行した最新版を指定
RUN go install github.com/air-verse/air@latest

# 依存関係のコピーとキャッシュ利用
COPY go.mod go.sum ./
ENV GOPROXY=https://proxy.golang.org,direct
RUN go env -w GOPROXY=$GOPROXY && go mod download

# ソースコードのコピー（airで監視するため）
COPY . .

# 起動時に air を実行
CMD ["air", "-c", ".air.toml"]