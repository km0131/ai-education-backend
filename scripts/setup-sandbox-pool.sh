#!/bin/bash
# setup-sandbox-pool.sh
#
# 生徒用サンドボックスコンテナのディスククォータを実現する「事前プロビジョニング
# 済みループバックプール」を、ホスト(ラズパイ)上に作成する。
#
# 背景: backendコンテナは /var/run/docker.sock を叩く非特権コンテナであり、
# backendの中で直接 mount/losetup してもホスト側のdockerdからは見えない
# (マウント名前空間が別)。backendに privileged を与えるのは多層防御の原則に
# 反するため採用しない。代わりに、このスクリプトを「ホスト上で」実行して
# 固定サイズのループバックファイルシステムをあらかじめ作成・マウントしておき、
# Goバックエンドは Docker Engine API の Binds でそのパスを bind mount するだけ、
# という役割分担にする(NextPlan.md §6)。
#
# 実行場所: backendコンテナの「中」ではなく、ラズパイ(Dockerホスト)上で直接、
# root権限で実行すること。
#
#   sudo ./setup-sandbox-pool.sh
#
# 設定: このリポジトリの .env にある以下の値を読む(3つともGoバックエンド側の
# 同名変数と値を揃えること。ズレるとバックエンドが存在しないスロット名を
# 参照してコンテナ作成に失敗する)。
#   SANDBOX_DISK_QUOTA_MB  … スロット1つあたりのサイズ(MB)。デフォルト 200
#   SANDBOX_POOL_SIZE      … スロット数(=同時にディスク割り当てできる生徒数)。デフォルト 10
#   SANDBOX_POOL_BASE_DIR  … プールを置くホストパス。デフォルト /var/sandboxes
#
# 冪等性: 既にマウント済みのスロットはスキップする。作成済みのスロットを
# 増やしたい場合(SANDBOX_POOL_SIZEを増やした場合)は、このスクリプトを
# 再実行するだけで不足分だけ追加作成される。
#
# 重要: ループバックマウントは再起動で消える。再起動のたびにこのスクリプトを
# 再実行しないと、プールのパスが「ただの空ディレクトリ」に戻ってしまい
# (mount忘れの状態でもDockerのbind mountはディレクトリを自動作成して普通に
# 成功してしまうため気づきにくい)、クォータが効かなくなる。systemdで
# 起動時に自動実行することを強く推奨する(例を末尾に記載)。

set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "エラー: root権限で実行してください(例: sudo $0)" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${ENV_FILE:-"$SCRIPT_DIR/../.env"}"

if [ -f "$ENV_FILE" ]; then
    # .env はKEY=VALUE形式のみを想定(このリポジトリの.envと同じ前提)。
    # 既にエクスポート済みの同名変数があればそちらを優先(CI等での上書き用)。
    while IFS='=' read -r key value; do
        [ -z "$key" ] && continue
        case "$key" in \#*) continue ;; esac
        if [ -z "${!key:-}" ]; then
            export "$key"="$value"
        fi
    done < <(grep -E '^[A-Za-z_][A-Za-z0-9_]*=' "$ENV_FILE")
fi

QUOTA_MB="${SANDBOX_DISK_QUOTA_MB:-200}"
POOL_SIZE="${SANDBOX_POOL_SIZE:-10}"
POOL_BASE_DIR="${SANDBOX_POOL_BASE_DIR:-/var/sandboxes}"
IMAGE_DIR="$POOL_BASE_DIR/.images"

for cmd in mkfs.ext4 losetup mountpoint fallocate; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "エラー: 必須コマンド '$cmd' が見つかりません(e2fsprogs/util-linuxをインストールしてください)" >&2
        exit 1
    fi
done

mkdir -p "$POOL_BASE_DIR" "$IMAGE_DIR"

echo "サンドボックス用ディスククォータプールを作成します"
echo "  プール置き場: $POOL_BASE_DIR (スロット ${POOL_SIZE}個, 各 ${QUOTA_MB}MB)"
echo

created=0
skipped=0

for i in $(seq -w 1 "$POOL_SIZE"); do
    # Goバックエンド(program_service.go の poolSlotName)と命名を一致させる: "pool-01" 等。
    # `seq -w` は開始値(01)の桁数に合わせてゼロ埋めするため、末尾に -f "%02d" で明示的に揃える。
    slot_name="pool-$(printf '%02d' "$((10#$i))")"
    mount_point="$POOL_BASE_DIR/$slot_name"
    image_file="$IMAGE_DIR/$slot_name.img"

    mkdir -p "$mount_point"

    if mountpoint -q "$mount_point"; then
        echo "  [スキップ] $slot_name は既にマウント済みです"
        skipped=$((skipped + 1))
        continue
    fi

    if [ ! -f "$image_file" ]; then
        echo "  [作成] $slot_name のイメージファイルを作成します (${QUOTA_MB}MB)"
        fallocate -l "${QUOTA_MB}M" "$image_file"
        mkfs.ext4 -q -F "$image_file"
    fi

    echo "  [マウント] $slot_name -> $mount_point"
    mount -o loop "$image_file" "$mount_point"
    created=$((created + 1))
done

echo
echo "完了: 新規マウント ${created}件 / 既存スキップ ${skipped}件 (合計 ${POOL_SIZE}スロット)"
echo
echo "重要: このマウントは再起動で消えます。起動時に自動実行するよう、"
echo "例えば以下のようなsystemdユニットを /etc/systemd/system/sandbox-pool.service として登録し、"
echo "'systemctl enable sandbox-pool.service' しておくことを推奨します:"
echo
cat <<'EOF'
  [Unit]
  Description=Mount sandbox disk-quota loopback pool
  Before=docker.service
  RequiredBy=docker.service

  [Service]
  Type=oneshot
  RemainAfterExit=yes
  ExecStart=/path/to/ai-education-backend/scripts/setup-sandbox-pool.sh

  [Install]
  WantedBy=multi-user.target
EOF
