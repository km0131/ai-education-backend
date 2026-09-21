from flask import Flask
from werkzeug.middleware.proxy_fix import ProxyFix

app = Flask(__name__)

# このアプリは preview.a-kiis.com/{公開用の合言葉}/... のように、URLの先頭に
# プレフィックス(飾り)が付いた状態でインターネットに公開されることがあります
# (「Webサイトを開く」ボタン、公開機能)。ProxyFix(x_prefix=1)を使うと、
# Flaskがそのプレフィックスを知った上で動くようになり、url_for()が作る
# リンクやリダイレクト先にも自動でプレフィックスが付くようになります。
# これが無いと、リンクをクリックした時にプレフィックスの外(存在しない
# ページ)を指してしまい、404になることがあります。
app.wsgi_app = ProxyFix(app.wsgi_app, x_prefix=1)


@app.route("/")
def index():
    return "Hello, Flask!"


if __name__ == "__main__":
    # host="0.0.0.0" にしないと、コンテナの外(プレビュー機能)からこの
    # アプリへアクセスできません。
    app.run(host="0.0.0.0", port=5000, debug=True)
