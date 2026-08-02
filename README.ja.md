# knowbrew

**コーディングエージェントは、セッションが終わると全部忘れます。**

同じ前提をまた説明する。同じミスをまた指摘する。先月解決したはずの問題をまた
調べ直す —— 答えが、閉じてしまったセッションの中にしか存在しなかったからです。

`knowbrew` は Claude Code / Codex のセッションログから知識を醸造し、あなたの
手元のフォルダにMarkdownとして蓄積します。次のセッションのエージェントは、
それを検索して使えます。

```sh
npm install -g knowbrew
knowbrew init
```

[English README](README.md)

## 何が得られるか

- **エージェントが自分で調べられるようになる** — 過去の決定・規約・指摘・解決策が
  検索可能なレコードになり、関連する場面でエージェント自身が読みに行きます。あなたが
  説明し直す必要がなくなります。
- **承認しないものは決してエージェントに届かない** — knowbrewが書くものはすべて
  `approved: false` から始まり、通常の検索には出ません。承認するかを決めるのは
  あなたです。誤った推論が混じっても無害なまま留まります。
- **ただのMarkdown、置き場所はあなたのフォルダ** — Obsidian Vaultでも任意の
  ディレクトリでも構いません。読む・編集する・grepする・同期する・消す、すべて自由。
  サービスもアカウントもロックインもありません。
- **Claude Code と Codex の両方で使える** — 知識ベースは一つ、エージェントは両方。
- **すべて出典まで遡れる** — どのレコードも、元になったターンを明示します。元の
  セッションログは変更も複製もしません。

## 仕組み

酒造のメタファーによる3ステップです。

```text
セッションログ ──draw──▶ feedstock ──brew──▶ knowledge ──▶ あなたが承認
(手を加えない)           (何が起きたか        (記憶する価値が
                          1ターン1件)          あること)
```

**`draw`** はセッションログを読み、1ターンにつき1件の *feedstock* を記録します。
短い要約と、そのターンで確定した個々の主張が入ります。生の対話はソースログに
置いたままで、feedstock はそこを指すだけです。

**`brew`** はその証拠を読み、*knowledge* を書きます。元のターンを越えて有用な
主張です。1件につき型（type）と対象（subject）が1つずつ付き、以降の変化に応じて
訂正・置換・統合されていきます。

**承認するのはあなた** — 新しい knowledge は `approved` が未チェックの状態で
作られます。Obsidian でチェックするか、Markdown を `approved: true` に書き換えて
ください。そこで初めてエージェントから見えるようになります。

## はじめかた

知識ファイルを置きたいディレクトリで `init` を実行します。Obsidian Vault の
フォルダが適しています。

```sh
knowbrew init
```

どのセッションログを読むか、どのLLMバックエンドを使うか、Claude Code / Codex に
自身を登録するかを対話で尋ねます。再実行すると現在の設定が初期値として読み込まれ、
尋ねなかった項目はそのまま維持されます。

そのうえで知識ベースを構築します。

```sh
knowbrew draw    # セッションログ → feedstock
knowbrew brew    # feedstock → 未承認のknowledge
```

どちらも冪等で、何度実行しても安全です。欠けているものだけを埋めるので、実行が
重なっても重複しません。フック・cron・手動、どこから走らせても構いません。

引数なしの `draw` は、直近24時間に更新された設定済みセッションファイルを対象に
します。`--since 7d`（またはRFC3339タイムスタンプ）で範囲を広げ、`--all` で
すべての履歴を対象にできます。

作られたものを確認し、エージェントに使わせたいものを承認します。

```yaml
# knowledge/kn-8f17c9a6b4d2e301.md
approved: true   # 変更前: false
```

`trigger: always` が付いたレコードは、`init` が登録する SessionStart フックに
よって毎セッションの冒頭に注入されます。それ以外はエージェントが検索で見つけます。

## 日常の使い方

知識を検索する:

```sh
knowbrew knowledge -- sqlite locking
knowbrew knowledge --subject myproject --type decision
```

実際に何があったかを振り返る:

```sh
knowbrew feedstock --last 10                      # 直近10ターン
knowbrew feedstock --subject myproject -- deploy  # デプロイをいじったのはいつ？
knowbrew show <feedstock-id> --raw                # 元の対話そのもの
```

キーワードは `--` の後に書きます。キーワードありなら関連度順、なしなら新しい順に
なるため、`feedstock` はそのまま作業の時系列として読めます。

エージェントも同じコマンドを使います。`init` が `CLAUDE.md` / `AGENTS.md` に
使い方を書き込むので、いつ使うべきかをエージェントが判断できます。

### トークン使用量

`draw` / `brew` の実行中、進捗行に入力・出力トークンの累計が表示されます
（`in ... / out ...`）。最終JSONの `usage` オブジェクトには、バックエンド・
モデル・種別ごとのトークン数が入ります。利用中のプロバイダの単価を掛ければ
コストが出せます。プロバイダの価格はCLIとは独立に変わるため、knowbrew は
価格表を同梱しません。

## 知識の型（type）

knowledge は必ず1つの型を持ちます。型は `masters/types/` 配下のマスターノートで、
そのファイル名が `--type` に渡せる値です。`init` は次の7つを作成します。

| 型 | 何を保持するか |
|---|---|
| `definition` | 用語や概念の、確立した意味や境界 |
| `property` | 持続的で確立した属性・能力（一時的な設定、実行結果、タスク状態は除く） |
| `relation` | 対象や概念のあいだの、確立した関係 |
| `principle` | 確立した一般化された原因・機序・反復的傾向 |
| `constraint` | 外部から課された、確立した制限や必要条件 |
| `decision` | 現在のタスクを越えて維持される意図をもつ決定（暫定的・一回限りの調整は除く） |
| `preference` | 安定した表明済みの選好（一回限りの要望や拘束力ある決定ではない） |

型はフィルタとして便利で（`--type decision` で決定事項を見直す）、醸造の精度も
保ちます。ある主張は、ちょうど1つの型に当てはまるときにだけ記録されます。

マスターの文言を編集したり、型を追加・削除したりできます。デフォルトが再生成
されるのは `masters/types/` に型ノートが1つも無いときだけで、1つでも残っていれば
knowbrew はそのディレクトリに触れません。

## 対象（subject）

subject は `masters/subjects/` 配下のマスターノートとして保存される、安定した
対象名です。knowbrew が自動で追加するのは、Gitリポジトリから機械的に導出できる
場合のみで、リモートURLと作業ディレクトリをそのマスターのエイリアスとして記録
します。

subject ノートには `definition`・`includes`・`excludes` を書けます。これらが
その subject の境界を決めます。いずれも無い場合は名前だけが手がかりになり、
`excludes` に該当すれば名前が一致しても除外されます。

subject を作れるのはあなただけです。knowbrew が勝手に作ることはありません。
未知の `--subject` はエラーで、`--new-subject` のようなフラグもありません。
作成・改名・統合・削除は Vault 上で直接行ってください。どの subject にも
該当しなかった主張は保留されるので、後から subject を整えれば対象になります。

## 設定

`init` は `<root>/.knowbrew/config.toml` を書き出します。

```toml
root = ".."

[llm]
backend = "claude-cli"    # または codex-cli, api, ollama
draw_model = ""           # ターンごとの分類: 速いモデルが向く
brew_model = ""           # 知識の判断: 強いモデルが向く
draw_effort = "low"       # 反復する分類処理: initの既定は low
brew_effort = ""          # 空ならバックエンド/ユーザーの既定に従う
timeout = "5m"

[draw]
concurrency = 5           # 並列LLMワーカー数
context_turns = 3         # 抽出時に渡す先行ターン数
max_context_turns = 20    # 上限付きのフォールバック窓

[[sources]]
agent = "claude"
path = "/Users/example/.claude/projects"
parser = "claude"
```

モデル名が空ならCLIバックエンド自身の既定を使います。`api` と `ollama` は両方の
モデル指定が必須で、認証情報は環境変数から読みます。

```sh
export KNOWBREW_API_URL=https://api.example.com/v1/chat/completions
export KNOWBREW_API_KEY=...
```

キーは環境変数からのみ読み、知識ルートに書き込まれることはありません。
`~/.config/knowbrew/location.toml` にルートの場所が記録されるので、どのディレクトリ
からでも knowbrew を実行できます。

CLIバックエンドはあなた自身のエージェントを起動するため、`CLAUDE.md` /
`AGENTS.md` がそのまま効きます。knowbrew が書く内容も、記述言語を含めてあなたの
指示に従います。これらのバックグラウンドジョブでは MCP サーバーは読み込まれず、
knowbrew 自身の SessionStart フックも発火しません。

## 必要なもの

- LLMバックエンドが1つ: Claude Code CLI、Codex CLI、ツール呼び出しに対応した
  OpenAI互換エンドポイント、またはツール対応モデルを持つ Ollama
- ソースからビルドする場合のみ Go 1.25 以降（SQLite FTS5 は純Go実装のため
  CGO は不要）

その他のインストール方法:

```sh
go install github.com/siro33950/knowbrew/cmd/knowbrew@latest
```

ビルド済みバイナリは
[GitHub Releases](https://github.com/siro33950/knowbrew/releases) にあります。

## コマンド一覧

```text
knowbrew init                      対話セットアップ
knowbrew draw [flags] [path...]    セッションログ → feedstock
knowbrew brew [--verbose]          feedstock → 未承認のknowledge
knowbrew knowledge [keywords...]   knowledgeを検索（別名: kn）
knowbrew knowledge show <id...>    任意の状態のknowledgeを表示
knowbrew feedstock [keywords...]   feedstockを検索・再生
knowbrew show <id...>              feedstock 1件; --raw で元の対話
```

検索の共通フラグ: `--subject`・`--type`・`--since`・`--until`・`--limit`・
`--max-tokens`・`--reindex`。`knowledge` にはさらに `--include-pending`・
`--include-retired`・`--trigger always`。`feedstock` にはさらに `--session`・
`--agent`・`--last N`。

`draw` のフラグ: `--all`・`--since`・`--until`・`--source claude|codex`・
`--verbose`。パスを明示した場合は、`--since` / `--until` を併用しない限り
時間で絞り込まれません。

LLMバックエンドが呼び出すためだけのサブコマンドもあり、それらは直接実行する
用途を想定していません。

## セキュリティ

セッションログは読み取り専用で、生成された知識はあなたがチェックするまで未承認の
ままです。APIキーは環境変数からのみ読み込まれます。信頼モデルと脆弱性の報告先は
[SECURITY.md](SECURITY.md) を参照してください。

## 開発

```sh
go test ./...
go vet ./...
gofmt -l .
```

実ログを複製せずにパーサーを検証する:

```sh
KNOWBREW_TEST_CLAUDE_LOG=/path/to/session.jsonl \
KNOWBREW_TEST_CODEX_LOG=/path/to/rollout.jsonl \
go test -v ./internal/parser -run TestRealLogWhenConfigured
```

リリースはメンテナがバージョンタグをpushして行います。通常のpushではテストのみ
実行されます。

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
```

## ライセンス

MIT
