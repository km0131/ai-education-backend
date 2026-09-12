package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	"ai-education/backend/internal/docker"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// pythonTunnelScriptTemplate is exec'd inside the sandbox container to relay
// raw bytes between this process's stdin/stdout and a plain TCP connection to
// 127.0.0.1:<port>(IDE内蔵Webプレビュー、VS CodeのSimple Browser相当)。
//
// なぜこれが必要か: backendサービス自体はsandbox-netに参加していない
// (docker-compose.yml、NextPlan.md §3.3 - 万一の生徒コンテナブレイクアウト
// 時にも本番DB/推論APIへ到達できないようにする多層防御の必須要件)。そのため
// backendプロセスがコンテナのブリッジネットワークIPへ直接HTTP接続すること
// はできない(できてしまうとネットワーク分離が無意味になる)。代わりに、
// シェル/LSP中継(shell_service.go/lsp_service.go)と全く同じ「Docker Engine
// APIのexec」経由でコンテナ内に入り、そこから127.0.0.1(コンテナ自身の
// ネットワーク名前空間)へ接続する - この経路はsandbox-netへの到達性を一切
// 必要としない。pythonはベースイメージに標準搭載済み(sandbox-base/Dockerfile)
// なので追加インストール不要。
// stdin側はos.read()(低レベルの生read(2)呼び出し)を使う点が重要 -
// sys.stdin.buffer.read(65536)(BufferedReader.read)は「65536バイト溜まる
// かEOFになるまで返らない」ため、HTTPリクエストのように65536バイトより
// ずっと小さいデータが1回分だけ届いて、その後はサーバーの応答を待つだけ
// (クライアント側がstdinを閉じるわけではない)というケースで永遠にブロック
// してしまう(実際にこれが原因でプレビューがブロックしていた - 1リクエスト
// 分の小さなリクエストは転送されず、上流のFlaskには何も届かないため応答も
// 返らず、画面が白いまま固まっていた)。os.read()は届いている分だけ即座に
// 返す(生のsocket.recvと同じセマンティクス)ため、この問題が起きない。
const pythonTunnelScriptTemplate = `import socket, sys, os, threading
PORT = %d
try:
    s = socket.create_connection(("127.0.0.1", PORT), timeout=5)
except OSError as e:
    # 標準出力には絶対に書かない(Go側はこのプロセスの標準出力をそのまま
    # HTTPレスポンスのバイト列として読むため、ここに書くとHTTPパースが
    # 壊れる)。標準エラーだけに書く - StartPreviewTunnel(preview_service.go)
    # がexec終了後にまとめて回収し、サーバーログに残す。ポート未指定/
    # アプリ未起動等でよく起きる「接続先に何も無い」状態を、502だけでは
    # 分からない具体的な理由(何のportに何が起きたか)として残すため。
    print("preview-tunnel: connect to 127.0.0.1:%%d failed: %%s" %% (PORT, e), file=sys.stderr)
    sys.exit(1)
def pump_in():
    try:
        while True:
            b = os.read(0, 65536)
            if not b:
                break
            s.sendall(b)
    finally:
        try:
            s.shutdown(socket.SHUT_WR)
        except OSError:
            pass
threading.Thread(target=pump_in, daemon=True).start()
while True:
    b = s.recv(65536)
    if not b:
        break
    sys.stdout.buffer.write(b)
    sys.stdout.buffer.flush()
`

// previewTunnelConn adapts the hijacked exec connection from
// StartPreviewTunnel into a plain net.Conn so it can be returned directly
// from an http.Transport's DialContext. Write/Close/LocalAddr/RemoteAddr/
// SetDeadline等は埋め込んだnet.Connにそのまま委譲する(execの標準入力への
// 書き込みとして機能する)。Read()だけは上書きする必要がある - execAttachの
// 生の読み取り側はDockerの多重化フレーム(標準出力/標準エラー混在、8バイト
// ヘッダー)そのままなので、stdcopy.StdCopyで復元した後のバイト列(=標準
// エラーを捨てた、標準出力=TCPからの応答そのもの)をio.Pipe経由で読ませる。
type previewTunnelConn struct {
	net.Conn
	stdout *io.PipeReader
}

func (c *previewTunnelConn) Read(p []byte) (int, error) {
	return c.stdout.Read(p)
}

func (c *previewTunnelConn) Close() error {
	err := c.Conn.Close()
	_ = c.stdout.Close()
	return err
}

// StartPreviewTunnel execs the relay script above inside containerID and
// returns a net.Conn that behaves like a direct TCP connection to
// 127.0.0.1:port from the container's own network namespace - see
// pythonTunnelScriptTemplateのコメント参照。呼び出し元(NewPreviewReverseProxy
// のDialContext)が1リクエストにつき1本ずつ張り、レスポンスを読み終えたら
// 閉じる想定(HTTPのkeep-alive/接続の使い回しはしない - シンプルさ優先。
// ページ内のアセット数だけexecが増えるが、教育用サンドボックスの規模では
// 許容できるオーバーヘッドと判断している)。
func StartPreviewTunnel(ctx context.Context, cli docker.ContainerAPI, containerID string, port int) (net.Conn, error) {
	script := fmt.Sprintf(pythonTunnelScriptTemplate, port)
	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"python3", "-c", script},
		Tty:          false,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, err
	}

	hijacked, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	go func() {
		var stderrBuf bytes.Buffer
		_, copyErr := stdcopy.StdCopy(pw, &stderrBuf, hijacked.Reader)
		// 中継スクリプト自身が標準エラーへ何か書いていた場合(典型的には
		// 上記pythonTunnelScriptTemplateの接続失敗時のメッセージ)、502に
		// なった時にサーバーログだけからでも原因(どのport/containerで何が
		// 起きたか)を追えるようにする。ブラウザ側の応答には含めない -
		// previewErrorHandlerが返すのは常に汎用メッセージのまま。
		if stderrBuf.Len() > 0 {
			log.Printf("[PREVIEW-TUNNEL] container=%s port=%d の中継スクリプトが標準エラーへ出力しました: %s", containerID, port, strings.TrimSpace(stderrBuf.String()))
		}
		_ = pw.CloseWithError(copyErr)
	}()

	return &previewTunnelConn{Conn: hijacked.Conn, stdout: pr}, nil
}

// NewPreviewReverseProxy builds a reverse proxy that forwards one request to
// http://127.0.0.1:<port><targetPath>(クエリ文字列は元のリクエストのものが
// そのまま使われる、httputil.ReverseProxyの標準動作)、実際の接続確立は
// TransportのDialContextでStartPreviewTunnelに差し替えている - 生の
// net.Dialは一切行わない(backendがsandbox-netへ到達できる必要が無い設計、
// StartPreviewTunnelのコメント参照)。
//
// パスプレフィックス方式の既知の制約: Flaskの`url_for()`等が生成する絶対
// パス(例: `/static/style.css`)は、このAPI自身のプレフィックス配下に
// 居ることを知らないため、そのままではプレフィックスの外(このAPIの
// ルート)を指してしまうことがある。HTMLレスポンスには<base href>を注入し
// 同一ディレクトリ内の相対参照だけは補正しているが(injectBaseHref)、
// 絶対パスの完全な解決にはNextPlan.mdフェーズ7のようなサブドメイン単位の
// ルーティングが必要になる - シンプルな(相対パス中心の)ページのプレビュー
// 用と割り切る。
func NewPreviewReverseProxy(cli docker.ContainerAPI, containerID, targetPath string, port int, basePath string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: newTunnelTransport(cli, containerID, port),
		Director:  newTunnelDirector(targetPath, port),
		ModifyResponse: func(resp *http.Response) error {
			_, err := injectBaseHref(resp, basePath)
			return err
		},
		ErrorHandler: previewErrorHandler,
	}
}

// NewPublicPreviewReverseProxy is NewPreviewReverseProxy's counterpart for
// the単一サブドメイン(preview.a-kiis.com)パスベース公開(NextPlan.md
// フェーズ7、public_preview_handler.go) - 中身はほぼ同じだが、応答本文に
// 対して簡易コンテンツスキャン(ScanResponseBodyForAbuse、content_scan.go)
// も追加でかける。認証済みセッション限定のIDE内蔵プレビュー
// (NewPreviewReverseProxy)には不要な処理のため、別関数として分けている。
func NewPublicPreviewReverseProxy(cli docker.ContainerAPI, containerID, targetPath string, port int, basePath, slug string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: newTunnelTransport(cli, containerID, port),
		Director:  newTunnelDirector(targetPath, port),
		ModifyResponse: func(resp *http.Response) error {
			body, err := injectBaseHref(resp, basePath)
			if err != nil {
				return err
			}
			if body != nil {
				ScanResponseBodyForAbuse(body, slug)
			}
			return nil
		},
		ErrorHandler: previewErrorHandler,
	}
}

// newTunnelTransport/newTunnelDirector factor out the exec-tunnel wiring
// shared by NewPreviewReverseProxy/NewPublicPreviewReverseProxy above.
func newTunnelTransport(cli docker.ContainerAPI, containerID string, port int) *http.Transport {
	return &http.Transport{
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return StartPreviewTunnel(dialCtx, cli, containerID, port)
		},
		// 1リクエストにつき1本のexecトンネルを張って閉じる方針(StartPreviewTunnel
		// のコメント参照)のため、コネクションプールによる使い回しはしない。
		DisableKeepAlives: true,
	}
}

func newTunnelDirector(targetPath string, port int) func(*http.Request) {
	return func(req *http.Request) {
		req.URL.Scheme = "http"
		// DialContextは第2引数(addr)を無視して常にcontainerID+portへ
		// トンネルするため、ここは実際のダイヤル先ではなくHostヘッダー/
		// ログ表示上の値として使われるだけ。
		req.URL.Host = fmt.Sprintf("127.0.0.1:%d", port)
		req.URL.Path = targetPath
		req.Host = req.URL.Host
	}
}

var previewErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
	// 実際の理由(EOF、コンテナのexecトンネル自体の起動失敗、接続拒否等)は
	// ブラウザへは返さず(生徒に内部実装の詳細を見せる必要は無い)、サーバー
	// ログにだけ残す - 502だけでは「そもそも何が失敗したか」が分からず、
	// 実際にこの種の問い合わせ(生徒がポート番号を間違えた/アプリを起動し
	// 忘れた等)の原因調査で困ったため追加した。
	log.Printf("[PREVIEW-PROXY-ERROR] %s %s: %v", r.Method, r.URL.Path, err)
	// このレスポンスはiframeのsrc(WebPreviewPane.tsx)またはpreview.a-kiis.com
	// への直接ナビゲーションとして、ブラウザがそのままトップレベルの
	// ページとして描画する - 以前はJSONを返しており、装飾の無いテキストが
	// そのまま白紙同然の画面に表示されるだけで「実行していないだけ」なのか
	// 「本当に壊れている」のか生徒に伝わらなかった(「画面が白いまま」の
	// 問い合わせの一因)。案内文つきのHTMLにして、次に取るべき行動
	// (実行してから再読み込み)を明示する。
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = w.Write([]byte(previewErrorPageHTML))
}

const previewErrorPageHTML = `<!doctype html>
<html lang="ja"><head><meta charset="utf-8"><title>プレビューに接続できません</title>
<style>
  body { margin:0; height:100vh; display:flex; align-items:center; justify-content:center;
    background:#1e1e1e; color:#cccccc; font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif; }
  .box { max-width: 420px; padding: 24px; text-align:center; }
  .icon { font-size: 32px; margin-bottom: 12px; }
  h1 { font-size: 15px; margin: 0 0 8px; color:#ffffff; }
  p { font-size: 13px; line-height: 1.7; color:#a0a0a0; margin: 0; }
</style></head>
<body>
  <div class="box">
    <div class="icon">🔌</div>
    <h1>プレビュー対象への接続に失敗しました</h1>
    <p>アプリがまだ起動していないか、指定したポートで待ち受けていない可能性があります。<br>
    ターミナルで「実行」してからもう一度読み込み直してください。</p>
  </div>
</body></html>`

// injectBaseHref rewrites an HTML response body to add
// `<base href="{basePath}">` right after <head>(無ければ<html ...>の直後、
// それも無ければ先頭)。ページ内の相対パス参照(スクリプト/CSS/画像/同一
// ディレクトリ内リンク)をこのプレフィックス配下として解決させるため。
// 絶対パス(先頭"/")の参照までは補正できない(NewPreviewReverseProxyの
// コメント参照)。戻り値は最終的な本文バイト列(HTML以外ならnil) -
// 呼び出し元(NewPublicPreviewReverseProxy)がコンテンツスキャンにそのまま
// 再利用し、resp.Bodyを二重に読むのを避けるため。
func injectBaseHref(resp *http.Response, basePath string) ([]byte, error) {
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/html") {
		return nil, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()

	baseTag := []byte(`<base href="` + basePath + `">`)
	lower := bytes.ToLower(body)
	var newBody []byte
	if idx := bytes.Index(lower, []byte("<head>")); idx != -1 {
		insertAt := idx + len("<head>")
		newBody = concatBytesSlices(body[:insertAt], baseTag, body[insertAt:])
	} else if idx := bytes.Index(lower, []byte("<html")); idx != -1 {
		if end := bytes.IndexByte(body[idx:], '>'); end != -1 {
			insertAt := idx + end + 1
			newBody = concatBytesSlices(body[:insertAt], baseTag, body[insertAt:])
		} else {
			newBody = concatBytesSlices(baseTag, body)
		}
	} else {
		newBody = concatBytesSlices(baseTag, body)
	}

	resp.Body = io.NopCloser(bytes.NewReader(newBody))
	resp.ContentLength = int64(len(newBody))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(newBody)))
	return newBody, nil
}

func concatBytesSlices(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
