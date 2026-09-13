FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
# LANGが未設定だとPOSIX/Cロケールになり、bashのreadlineが日本語などの
# マルチバイト文字を「1文字」ではなく「複数バイト」として扱ってしまう
# (例: 日本語入力後にBackspaceを押すとバイト単位で消えて表示が崩れる)。
# C.utf8はglibc組み込みのUTF-8ロケールで、追加パッケージ(locales)なしで
# 使える(`locale -a`で確認済み)。
ENV LANG=C.utf8
ENV LC_ALL=C.utf8

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    python3-venv \
    git \
    ca-certificates \
    curl \
    libatomic1 \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir --break-system-packages pyright

RUN pip install --no-cache-dir --break-system-packages \
    flask==3.1.3 \
    flask-sqlalchemy==3.1.1 \
    requests==2.34.2 \
    python-dotenv==1.2.3

# コンテナはroot実行のまま運用する。
# セキュリティ境界はunixのuid分離ではなくgVisor(Sentry)自体に委ねる
# (gVisor環境ではsudo等のsetuid権限昇格が機能しないため)
WORKDIR /root

RUN pyright --version

# git: `git init`はコンテナ起動時にentrypoint.shが自動実行する(下記COPY
# entrypoint.sh参照)ので、生徒は最初からコミットできる状態になる。
# identity未設定だと最初の`git commit`が「Please tell me who you are」で
# 失敗するため、動く状態のデフォルトとして汎用のidentityを設定しておく
# (生徒は`git config`で上書き可能)。init.defaultBranch=mainはgitの警告
# (hint: ...master...)を出さないための最近のデフォルトへの追従。
RUN git config --global user.name "Student" \
    && git config --global user.email "student@sandbox.local" \
    && git config --global init.defaultBranch main

# 生徒向けの案内README。/root/workspace自体はコンテナ生成時にホスト側の
# ディスククォータプールスロットがbind mountされる場所(program_service.go の
# workspaceBind)なので、ここへ直接COPYしても実行時には見えなくなってしまう
# (bind mountがイメージ側の内容を覆い隠すため)。そのためbind mountされない
# 別の場所に置いておき、起動時にentrypoint.shが「まだ無ければ」コピーする
# 方式にする(生徒がファイルを削除/編集した後の再開時に上書きしないため)。
COPY workspace-init/README.md /opt/workspace-init/README.md

# 汎用LSP中継(internal/service/lsp_service.go)が拡張子→起動コマンドを
# 引く対応表のデフォルト値。Pythonは何も設定しなくても自動でpyrightが
# 起動するよう、コンテナ生成時にあらかじめ書き込んでおく(NextPlan.md
# フェーズ6)。他言語を使う生徒は、このファイルに自分でエントリを追記する。
COPY workspace-init/lsp-config.json /opt/workspace-init/lsp-config.json

# エントリポイントスクリプト: DNS上書き・ワークスペース初期化・git init・
# Readyフラグ作成までを1つのスクリプトに集約している(entrypoint.sh参照 -
# 以前はDockerfile内にprintfで直接埋め込んだ短い一行だったが、ステップが
# 増えて読みにくくなったため実ファイルに切り出した)。
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
# コンテナのメインプロセスはただ生き続けるだけでよい。対話シェル自体は
# `docker exec`(shell_service.go の ContainerExecCreate/Attach)で別途
# `bash`を起動する完全に独立したプロセスなので、ここは無関係
# (program_service.goのContainerCreateもstdin/ttyを繋がない)。
# 以前は`bash`をそのままCMDにしていたため、stdinが繋がらず即座にEOFを
# 読んでコンテナが起動直後(1秒未満)に正常終了(exit 0)してしまい、
# ファイル一覧/シェルWebSocketが「サンドボックスが起動していません」に
# なる原因になっていた。
CMD ["sleep", "infinity"]
