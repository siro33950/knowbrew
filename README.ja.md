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

酒造のメタファーに沿って処理します。

```text
セッションログ ─draw─▶ feedstock ─brew─▶ knowledge ─承認─▶ distill ─▶ documents
(手を加えない)          (何が起きたか)       (持続する主張)             (派生文書)
```

**`draw`** はセッションログを読み、1ターンにつき1件の *feedstock* を記録します。
短い要約と、そのターンで確定した個々の主張が入ります。生の対話はソースログに
置いたままです。feedstockにはエージェントとセッションIDだけを保存し、原文が必要な
ときは設定済みソースから現在の配置場所を解決します。

**`brew`** はその証拠を読み、*knowledge* を書きます。元のターンを越えて有用な
主張です。1件につき型（type）と対象（subject）が1つずつ付き、以降の変化に応じて
訂正・置換・統合されていきます。

**承認するのはあなた** — 新しい knowledge は `approved` が未チェックの状態で
作られます。Obsidian でチェックするか、Markdown を `approved: true` に書き換えて
ください。そこで初めてエージェントから見えるようになります。

**`distill`** は、承認済みかつ現行のknowledgeからsubjectごとの読み物を再生成します。
各文書はsubjectに割り当てたTemplateに従い、実際に利用したKnowledge IDを記録します。
正本はknowledgeであり、`documents/` 配下は再生成できる派生物です。

## はじめかた

知識ファイルを置きたいディレクトリで `init` を実行します。Obsidian Vault の
フォルダが適しています。

```sh
knowbrew init
```

どのセッションログを読むか、どのLLMバックエンドを使うか、Claude Code / Codex に
自身を登録するかを対話で尋ねます。登録すると、承認済みKnowledgeを読むSessionStart
フックと、完了したセッションターンをDrawするStopフックが追加されます。再実行すると
現在の設定が初期値として読み込まれ、尋ねなかった項目はそのまま維持されます。

そのうえで知識ベースを構築します。

```sh
knowbrew draw    # セッションログ → feedstock
knowbrew brew    # feedstock → 未承認のknowledge
# knowledgeを確認・承認した後:
knowbrew distill # 承認済みknowledge → subject文書
```

各コマンドは何度実行しても安全です。DrawとBrewは未完了の処理を再開し、Distillは
派生文書を再生成します。フック・cron・手動、どこから走らせても構いません。

引数なしの `draw` は、直近24時間に更新された設定済みセッションファイルを対象に
します。`--since 7d`（またはRFC3339タイムスタンプ）で範囲を広げられます。
`--max N` は、設定済みの全履歴から未完了ターンを最大N件だけ選びます。
取得済みの未完了ターンを先に再開し、その後は新しいターンから古いターンへ進むため、
cronなどで繰り返せばLLM処理量を制限したまま過去データを消化できます。

```sh
knowbrew draw --max 100
knowbrew brew --max 100
knowbrew distill --max 2
```

drawのサマリには、今回の対象数を`turns_selected`、対象範囲に残る未完了数を
`turns_pending`として出力します。
Brewの`--max`は未処理Assertionを数え、機械NOOPは上限を消費しません。Distillでは
Subject文書を数えます。上限付きDistillは次回、次のSubjectとTemplateから続行するため、
各文書が再生成可能なままでも、繰り返し実行すれば割り当て済み文書を順番に処理できます。

作られたものを確認し、エージェントに使わせたいものを承認します。

```yaml
# knowledge/kn-8f17c9a6b4d2e301.md
approved: true   # 変更前: false
```

`init` が登録する SessionStart フックは `knowbrew context` を実行します。
テンプレートが `inject: always` を宣言するdistill済み文書と、現在の作業ディレク
トリにaliasesが一致したsubjectの文書(`inject: subject`。デフォルトでは
`decisions` テンプレートが宣言)が毎セッションの冒頭に注入されます。文書は承認
済みknowledgeのみから生成され、注入量は `[context] max_tokens` で制限されます。
それ以外はエージェントが検索で見つけます。

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

意味検索が有効なら、キーワード付き検索は FTS5 とベクトル検索を並行実行し、RRFで
順位を統合します。knowledge は Claim だけ、feedstock は Summary だけをベクトル化
します。subject・type・ライフサイクル・agent・session・期間のフィルタは引き続き
完全一致です。検索スコアは確信度ではないため返しません。片方だけを診断するときは
`--search-mode text` / `--search-mode vector` を使えます。既定は `hybrid` です。

エージェントも同じコマンドを使います。`init` が `CLAUDE.md` / `AGENTS.md` に
使い方を書き込むので、いつ使うべきかをエージェントが判断できます。

### トークン使用量

`draw` / `brew` / `distill` の実行中、進捗行に入力・出力トークンの累計が表示されます
（`in ... / out ...`）。最終JSONの `usage` オブジェクトには、バックエンド・
モデル・種別ごとのトークン数が入ります。利用中のプロバイダの単価を掛ければ
コストが出せます。プロバイダの価格はCLIとは独立に変わるため、knowbrew は
価格表を同梱しません。

## 知識の型（type）

knowledge は必ず1つの型を持ちます。型は `masters/types/` 配下のマスターノートで、
そのファイル名が `--type` に渡せる値です。`init` は次の8つを作成します。

| 型 | 何を保持するか |
|---|---|
| `definition` | 用語や概念の、確立した意味や境界 |
| `property` | 持続的で確立した属性・能力（一時的な設定、実行結果、タスク状態は除く） |
| `relation` | 対象や概念のあいだの、確立した関係 |
| `principle` | 確立した一般化された原因・機序・反復的傾向 |
| `constraint` | 外部から課された、確立した制限や必要条件 |
| `decision` | 現在のタスクを越えて維持される意図をもつ決定（暫定的・一回限りの調整は除く） |
| `intent` | 現在の実現手段から独立して、対象・規則・設計が存在する理由を示す持続的な目的や品質 |
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

subject ノートには `definition`・`includes`・`excludes`・`documents` を書けます。
`documents` は、そのsubjectについて生成すべき文書を宣言するリストです。各値は
`masters/templates/` 配下の対応する定義へのwikilinkで、空ならそのsubjectは蒸留対象外です。
その他の項目がsubjectの境界を決めます。いずれも
無い場合は名前だけが手がかりになり、`excludes` に該当すれば名前が一致しても
除外されます。

subject を作れるのはあなただけです。knowbrew が勝手に作ることはありません。
未知の `--subject` はエラーで、`--new-subject` のようなフラグもありません。
作成・改名・統合・削除は Vault 上で直接行ってください。どの subject にも
該当しなかった主張は保留されるので、後から subject を整えれば対象になります。

## 蒸留文書

`init` は `concept`・`reference`・`decisions`・`glossary` の4つのTemplateマスターを
作成します。Templateは文書の目的・読者・対象範囲・完成条件・出力ファイル名・
Markdown構造を定義します。subjectノートで生成対象を次のように宣言します。

```yaml
documents:
  - "[[concept]]"
  - "[[reference]]"
```

`knowbrew distill` は、subjectに宣言された各文書について承認済みかつ現行の
Knowledgeを全件確認し、`documents/<subject>/<template-output>` を書きます。有効な
根拠Knowledgeがなくなった既存文書は削除します。`--subject` / `--template` で
実行対象を絞れます。

## 設定

`init` は `<root>/.knowbrew/config.toml` を書き出します。

```toml
root = ".."

[llm]
backend = "claude-cli"    # または codex-cli, api, ollama
draw_model = ""           # ターンごとの分類: 速いモデルが向く
brew_model = ""           # 知識の判断: 強いモデルが向く
distill_model = ""        # 文書の蒸留: 強いモデルが向く
draw_effort = "low"       # 反復する分類処理: initの既定は low
brew_effort = ""          # 空ならバックエンド/ユーザーの既定に従う
distill_effort = "high"   # 文書の選別・生成
timeout = "5m"

[draw]
concurrency = 5           # 並列LLMワーカー数
context_turns = 3         # 抽出時に渡す先行ターン数
max_context_turns = 20    # 上限付きのフォールバック窓

[embedding]
model = "ruri-v3-130m-int8-onnx" # または snowflake..., qwen3..., disabled, custom
# path = "/absolute/path/to/model" # custom の場合のみ必須

[[sources]]
agent = "claude"
parser = "claude"
paths = ["/Users/example/.claude/projects"]

[[sources]]
agent = "codex"
parser = "codex"
paths = [
  "/Users/example/.codex/sessions",
  "/Users/example/.codex/archived_sessions",
]
```

1つのsourceは複数ディレクトリにまたがる論理的な集合です。`init`はCodexの通常
セッションとアーカイブ済みセッションの両方を設定します。feedstockは物理パスを
保存しないため、設定済みディレクトリ間でセッションが移動しても`show --raw`、
Drawの再開、Brewは壊れません。

モデル名が空ならCLIバックエンド自身の既定を使います。`api` と `ollama` は3つすべての
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
knowbrew 自身のフックは即時終了するため、DrawやKnowledge検索が再帰しません。

`init` では、日本語推奨のRuri・英語推奨のSnowflake・品質優先のQwen3・全文検索
のみ、の4つから選びます。管理対象のモデルと実行環境は knowbrew が取得・固定し、
`.knowbrew/state/models/` に置きます。設定済みの管理モデルや自前モデルが利用不能な
場合、エラーを隠して全文検索へ切り替えることはありません。

自前モデルは `model = "custom"` とし、`manifest.json` を含むディレクトリを
`path` に指定します。manifest の共通必須項目は `id`・`backend`・`dimension`・
`model_file` です。ONNXではさらに `tokenizer_file`・`runtime_file`・`input_names`・
`output_name`、llama.cppでは `executable_file` を使います。ファイルパスはmanifestを
置いたディレクトリからの相対パスです。

## 必要なもの

- LLMバックエンドが1つ: Claude Code CLI、Codex CLI、ツール呼び出しに対応した
  OpenAI互換エンドポイント、またはツール対応モデルを持つ Ollama
- ソースからビルドする場合のみ Go 1.25 以降とCコンパイラ（FTS5は純Go、
  sqlite-vecは公式Go bindingから静的リンク）

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
knowbrew brew [flags]              feedstock → 未承認のknowledge
knowbrew distill [flags]           承認済みknowledge → subject文書
knowbrew knowledge [keywords...]   knowledgeを検索（別名: kn）
knowbrew knowledge show <id...>    任意の状態のknowledgeを表示
knowbrew feedstock [keywords...]   feedstockを検索・再生
knowbrew document [keywords...]    distill済みsubject文書を検索
knowbrew context                   distill済み文書からセッション開始文脈を出力
knowbrew show <id...>              feedstock 1件; --raw で元の対話
knowbrew index sync|rebuild|status 派生検索索引の保守
```

検索の共通フラグ: `--subject`・`--type`・`--since`・`--until`・`--limit`・
`--max-tokens`・`--reindex`・`--search-mode`。`knowledge` にはさらに `--include-pending`・
`--include-retired`。`feedstock` にはさらに `--session`・
`--agent`・`--last N`。`document` はdistill済みsubject文書を検索し、`--type` の
代わりに `--template` を持ちます。

`draw` のフラグ: `--max N`・`--since`・`--until`・`--source claude|codex`・
`--verbose`。明示するファイルまたはディレクトリは、設定済みsourceの配下である
必要があります。`--since` / `--until` を併用しない限り時間では絞り込まれません。

`brew` のフラグ: `--max N`・`--verbose`。

`distill` のフラグ: `--max N`・`--subject`・`--template`・`--verbose`。

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
