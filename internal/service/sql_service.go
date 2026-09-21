package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"ai-education/backend/internal/docker"
)

// SQLQueryResult is the DB種別に依存しない共通レスポンス形(「マルチDB対応
// SQL実行&閲覧UI」作業指示書)。今回実装するのはSQLiteのみだが、将来
// Postgres/MySQLを追加してもフロント(SqlSandboxViewer.tsx)が同じ形で
// 扱えるようにしてある。Error はomitempty にせず、値が無い時は
// JSON上も明示的にnullを返す(指示書のレスポンス例通り)。
type SQLQueryResult struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
	Error   *string          `json:"error"`
}

const (
	// defaultSQLTarget: リクエストでdb_targetが省略された場合のデフォルト
	// (指示書のUI仕様のデフォルト値と揃える)。
	defaultSQLTarget = "app.db"

	// defaultSQLExecTimeoutSec: 「膨大な処理や無限ループクエリ」対策の
	// タイムアウト(指示書要件「5〜10秒程度」)。他のexec系タイムアウト
	// (sandboxExecTimeout等、program_service.go)と同じ、env変数で上書き
	// 可能なパターンに揃える。
	defaultSQLExecTimeoutSec = 10
)

// sqlExecTimeout reads SQL_EXEC_TIMEOUT_SEC from the environment, falling
// back to defaultSQLExecTimeoutSec when unset or invalid.
func sqlExecTimeout() time.Duration {
	seconds := defaultSQLExecTimeoutSec
	if v := os.Getenv("SQL_EXEC_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			seconds = n
		}
	}
	return time.Duration(seconds) * time.Second
}

// resolveSandboxSQLiteTarget validates dbTarget(リクエストのdb_target、
// ファイルパス)がサンドボックスのワークスペース配下に収まることを保証する
// - 生徒本人の自分のコンテナに対してであっても、"../../etc/passwd"の
// ような相対パスやワークスペース外の絶対パスを与えてコンテナ内の無関係な
// ファイルへsqlite3として読み書きさせられてしまわないようにするための
// 境界チェック(ResolveSandboxPath/ResolveSandboxRelativePathと同じ、既存の
// ファイルAPIが使っている境界)。
func resolveSandboxSQLiteTarget(dbTarget string) (string, error) {
	trimmed := strings.TrimSpace(dbTarget)
	if trimmed == "" {
		trimmed = defaultSQLTarget
	}
	if path.IsAbs(trimmed) {
		return ResolveSandboxPath(trimmed)
	}
	return ResolveSandboxRelativePath(trimmed)
}

// sqliteQueryScript: sandbox-baseに既に入っているpython3標準ライブラリの
// sqlite3モジュールだけで動く(追加パッケージのインストール不要)。
// クエリ本文は引数でもf-string埋め込みでもなく標準入力から読む
// (runContainerCommandWithStdin) - シェル文字列にもPythonソース文字列にも
// 一切連結しないことで、クエリ内容が原因でこのスクリプト自体の構文を
// 壊す(またはコマンドインジェクションを起こす)余地を構造的に無くしている。
// 成功・失敗のどちらでも例外を外へ投げず、必ず{"columns":...,"rows":...,
// "error":...}という1行のJSONを標準出力へ書く(失敗時に500でクラッシュ
// させず、フロントのエラー枠に文言を出す、という指示書の要件をここで
// 満たす)。
//
// 1回のリクエストで実行できるのは単一のSQL文のみ(sqlite3.Cursor.execute()
// の制約をそのまま使っている) - ";"区切りの複数文をまとめて実行したい
// 場合はexecutescript()もあるが、あちらはSELECTの結果行を返せないため、
// 「実行して結果を見る」という本機能の性質上、あえて採用していない。
const sqliteQueryScript = `
import sys, json, sqlite3

def encode(value):
    if isinstance(value, (bytes, bytearray)):
        return {"__blob__": value.hex()}
    return value

def main():
    db_path = sys.argv[1]
    query = sys.stdin.read()
    try:
        conn = sqlite3.connect(db_path, timeout=3)
        conn.execute("PRAGMA busy_timeout = 3000")
        cur = conn.cursor()
        cur.execute(query)
        if cur.description is not None:
            columns = [d[0] for d in cur.description]
            rows = [
                {columns[i]: encode(value) for i, value in enumerate(row)}
                for row in cur.fetchall()
            ]
        else:
            columns, rows = [], []
        conn.commit()
        conn.close()
        print(json.dumps({"columns": columns, "rows": rows, "error": None}))
    except Exception as e:
        print(json.dumps({"columns": [], "rows": [], "error": str(e)}))

main()
`

// ExecuteSandboxSQLite runs a single SQL statement against a SQLite database
// file inside the caller's own sandbox workspace, via docker exec(python3の
// sqlite3標準モジュール、上記sqliteQueryScript参照) - コンテナ間の生の
// TCP接続やネイティブDBドライバは使わない(sandbox-netはbackendコンテナ
// から到達できないネットワーク分離になっているため、preview/shell/LSPと
// 同じexecベースの経路しかそもそも技術的に成立しない)。
//
// 戻り値のerrorはGo/Docker側のインフラ的な失敗(exec自体が実行できない等)
// のみを表す - クエリの構文エラーやテーブル不存在などのSQLレベルの失敗は
// (errではなく)戻り値のSQLQueryResult.Errorに文言として入る。
func ExecuteSandboxSQLite(ctx context.Context, cli docker.ContainerAPI, containerID, dbTarget, query string) (*SQLQueryResult, error) {
	target, err := resolveSandboxSQLiteTarget(dbTarget)
	if err != nil {
		msg := "指定されたDBファイルのパスが不正です"
		return &SQLQueryResult{Columns: []string{}, Rows: []map[string]any{}, Error: &msg}, nil
	}

	execCtx, cancel := context.WithTimeout(ctx, sqlExecTimeout())
	defer cancel()

	stdout, stderr, exitCode, err := runContainerCommandWithStdin(
		execCtx, cli, containerID,
		[]string{"python3", "-c", sqliteQueryScript, target},
		[]byte(query),
	)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("SQL実行プロセスが異常終了しました(exit=%d)", exitCode)
		}
		return &SQLQueryResult{Columns: []string{}, Rows: []map[string]any{}, Error: &msg}, nil
	}

	var result SQLQueryResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		msg := "実行結果の解析に失敗しました"
		return &SQLQueryResult{Columns: []string{}, Rows: []map[string]any{}, Error: &msg}, nil
	}
	if result.Columns == nil {
		result.Columns = []string{}
	}
	if result.Rows == nil {
		result.Rows = []map[string]any{}
	}
	return &result, nil
}
