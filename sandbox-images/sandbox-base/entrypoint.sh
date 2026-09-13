#!/bin/bash
# サンドボックスコンテナの初期化を1箇所に集約する。以前はgit initだけ
# Goバックエンド側からdocker exec経由で別途実行していた(program_service.go
# のinitWorkspaceGitRepo)が、このENTRYPOINTの初期化と別々のタイミングで
# 走ることで、(1) 生徒のファイル一覧/シェル接続がENTRYPOINTの初期化完了前に
# 飛んでしまう競合、(2) 追加のdocker exec呼び出し自体のオーバーヘッド、が
# あったため、ワークスペース初期化をすべてここに一本化した。Goバックエンド
# 側は最後に作る/tmp/sandbox-readyの存在をポーリングしてから後続処理を
# 進める(waitForSandboxReady、internal/service/program_service.go)。
#
# 途中のどのステップが失敗しても、必ず最後までたどり着いてexec "$@"
# (sleep infinity)へ引き継ぐことを最優先する - あえて`set -e`は使わない。
# 一部の初期化が失敗しても致命的ではない(例えばgit initが無くてもシェル/
# エディター機能自体は使える)一方、`set -e`で万一途中終了してしまうと、
# PID 1(このスクリプト自身)ごとコンテナが即座に終了してしまい、その方が
# ずっと深刻な障害になる(以前のCMD直接指定でPID1が即座にexit 0していた
# 不具合と同じ種類の問題を、新しい初期化ステップの追加によって再発させない
# ため)。

# 0. 前回起動時のReadyフラグを消す。resume(停止済みコンテナの再起動)でも
#    このENTRYPOINTは毎回実行されるため、古いフラグが残ったままだと
#    waitForSandboxReadyが「今回分の初期化がまだ終わっていないのに、前回分の
#    フラグを見て完了と誤認する」レースが起き得る。
rm -f /tmp/sandbox-ready

# 1. DNSサーバーの上書き設定(sandbox-net + gVisorの組み合わせではDocker
#    内蔵DNS(127.0.0.11)に到達できないため、起動時に外部DNSへ強制的に
#    向け直す)
echo "nameserver 8.8.8.8" > /etc/resolv.conf 2>/dev/null || true
echo "nameserver 8.8.4.4" >> /etc/resolv.conf 2>/dev/null || true

# 2. ワークスペースディレクトリおよび初期ファイルの配置。`-f`チェックにより
#    2回目以降(resume時)は何もしない(生徒が削除/編集した後を上書きしない
#    ため)。
mkdir -p /root/workspace
[ -f /root/workspace/README.md ] || cp /opt/workspace-init/README.md /root/workspace/README.md 2>/dev/null || true

# 3. LSP設定ディレクトリおよび初期ファイルの配置(同上)。
mkdir -p /root/workspace/.ai
[ -f /root/workspace/.ai/lsp-config.json ] || cp /opt/workspace-init/lsp-config.json /root/workspace/.ai/lsp-config.json 2>/dev/null || true

# 4. Gitリポジトリの初期化(未初期化の場合のみ)。identity(user.name/
#    user.email)とinit.defaultBranchはこのイメージのビルド時にグローバル
#    設定済み(このDockerfile参照)。
[ -d /root/workspace/.git ] || git init /root/workspace >/dev/null 2>&1 || true

# 5. 初期化完了を示すフラグファイルの作成。Goバックエンド側の
#    waitForSandboxReadyがこの存在をポーリングしてから、ファイル一覧/
#    シェル接続等の後続処理を有効化する。
touch /tmp/sandbox-ready

# 6. PID 1として常駐。対話シェル自体は別途docker exec
#    (shell_service.goのContainerExecCreate/Attach)で起動する完全に独立した
#    プロセスなので、ここは無関係。
exec "$@"
