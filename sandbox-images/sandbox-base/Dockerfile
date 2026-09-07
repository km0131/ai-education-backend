FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

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

# resolv.conf書き換え用のエントリポイントスクリプト
# (sandbox-net + gVisorの組み合わせではDocker内蔵DNS(127.0.0.11)に
#  到達できないため、起動時に外部DNSへ強制的に向け直す)
RUN printf '#!/bin/bash\necho "nameserver 8.8.8.8" > /etc/resolv.conf 2>/dev/null || true\necho "nameserver 8.8.4.4" >> /etc/resolv.conf 2>/dev/null || true\nexec "$@"\n' > /usr/local/bin/entrypoint.sh \
    && chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["bash"]
