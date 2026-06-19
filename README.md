# AI Education Backend

## 概要

AI Education システムのバックエンド API サーバーです。

Go + Gin + PostgreSQL を利用して構築されており、認証機能、ユーザー管理機能、画像管理機能を提供します。

---

## 技術スタック

### Backend Framework

* Go 1.25
* Gin

### Database

* PostgreSQL 17
* SurrealDB

### ORM

* Gorm

### Authentication

* Argon2
* AES-GCM

### Development Tools

* Air（ホットリロード）

---

## ディレクトリ構成

```text
backend/
├─ Dockerfile
├─ .air.toml
├─ cmd/
│  └─ main.go
└─ internal/
   ├─ db/
   │  ├─ client.go
   │  ├─ image.go
   │  └─ user_repo.go
   ├─ handler/
   │  └─ handler.go
   ├─ model/
   │  └─ model.go
   └─ utils/
      ├─ auth.go
      └─ crypto.go
```

---

- ローカル（docker-compose）起動例：リポジトリ直下で実行する。

```
docker compose up --build
podman-compose up --build
```

- コンテナの停止
```
podman-compose down
```


- コンテナの削除
```
sudo docker-compose down
```

---

## APIコード生成

OpenAPI スキーマからコードを生成します。

```bash
make gen-api
```

生成元:

```text
openapi/schema.yaml
```

---

## 環境変数

### Database

```env
DB_HOST=
DB_PORT=
DB_USER=
DB_PASSWORD=
DB_NAME=
```

### Security

```env
APP_MASTER_KEY=
```

---

## APIエンドポイント

### 認証

| Method | Path    |
| ------ | ------- |
| GET    | /login  |
| POST   | /login  |
| GET    | /signup |
| POST   | /signup |

---

## DB構成

### PostgreSQL

用途:

* ユーザー管理
* 学習履歴
* 資格情報

### SurrealDB

用途:

* グラフデータ
* AI関連データ
* 関係性分析

---

## 開発フロー

ソースコード変更時は Air により自動リロードされます。

```bash
docker compose up backend
```

または

```bash
air -c .air.toml
```

---

## 本番環境

### API

https://ai-api.kiiswebai.com/

### Swagger

http://localhost:8080/swagger/index.html#/
