package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"ai-education/backend/internal/docker"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// workspaceZipScript: sandbox-baseに既に入っているpython3標準ライブラリの
// zipfile/osモジュールだけで動く(zipコマンド等の追加パッケージのインストール
// 不要 - sqliteQueryScript(sql_service.go)と同じ方針)。ワークスペース全体を
// 再帰的に走査してZIPを作り、そのままstdoutへ書き出す。
//
// zipfile.ZipFile(sys.stdout.buffer, ...)は非seekableなストリームへの書き込み
// (Python 3.6以降の「データ記述子」対応、圧縮後に初めてCRC/サイズが確定する
// のでファイル本体の後ろに追記する形式)で、ローカルで実際にunzip/Pythonの
// zipfileどちらでも問題なく読めることを確認済み。1ファイルの読み取りに
// 失敗しても(壊れたシンボリックリンク等)そのファイルだけスキップし、
// ダウンロード全体は失敗させない。
const workspaceZipScript = `
import sys, os, zipfile

def main():
    root = sys.argv[1]
    with zipfile.ZipFile(sys.stdout.buffer, "w", zipfile.ZIP_DEFLATED) as zf:
        for dirpath, dirnames, filenames in os.walk(root):
            for filename in filenames:
                full_path = os.path.join(dirpath, filename)
                arcname = os.path.relpath(full_path, root)
                try:
                    zf.write(full_path, arcname)
                except OSError:
                    continue

main()
`

// StreamSandboxWorkspaceZip execs workspaceZipScript inside containerID and
// writes its stdout(ZIPバイト列そのもの)directly to w as it arrives -
// runContainerCommand/runContainerCommandWithStdinと違い、bytes.Bufferへ
// 全体を溜め込んでから返すのではなく、stdcopy.StdCopyの出力先を直接HTTP
// レスポンス(呼び出し元のhandlerがc.Writerを渡す)にすることで、ワーク
// スペース全体をバックエンドのメモリに載せずにストリーミング転送する
// (100MB超のアップロードを1リクエストで受け付ける仕様(sandboxUploadFileSizeLimit)
// と同様、ワークスペースサイズはディスククォータの範囲で数百MB〜になり
// 得るため)。
//
// ctxにはあえて個別のタイムアウトを追加しない(sandboxExecTimeout等とは
// 違う) - ワークスペースが大きいほど生成に時間がかかるのは正常であり、
// 呼び出し元のHTTPリクエストのcontext(クライアント切断で自動キャンセル
// される)にそのまま従う。
func StreamSandboxWorkspaceZip(ctx context.Context, cli docker.ContainerAPI, containerID string, w io.Writer) error {
	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"python3", "-c", workspaceZipScript, sandboxWorkspacePath},
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	})
	if err != nil {
		return err
	}

	hijacked, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		return err
	}
	defer hijacked.Close()

	var stderrBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(w, &stderrBuf, hijacked.Reader); err != nil {
		return err
	}
	// ctxがキャンセルされた結果として接続が強制的に閉じられた場合、
	// stdcopy.StdCopyはそれを普通のEOFとして観測し、エラーを返さずに
	// 正常終了してしまうことがある(file_service.goのruncontainerCommand*
	// と同じ理由・同じ対処)。
	if err := ctx.Err(); err != nil {
		return err
	}

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return err
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("workspace zip failed (exit=%d): %s", inspect.ExitCode, strings.TrimSpace(stderrBuf.String()))
	}
	return nil
}
