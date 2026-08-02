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

酒造のメタファーによる3層構造です。

```text
セッションログ ──draw──▶ feedstock ──brew──▶ knowledge
(手を加えない)           (何が起きたか        (記憶する価値が
                          1ターン1件)          あること)
```

**`draw`** はセッションログを読み、完了または中断が確定した1ターンにつき1件の *feedstock* を記録します。
実行中の末尾ターンはソースログに残し、完了レコードが現れた後の次回drawで初めて取得します。
取得フェーズではソースの識別情報と環境に関する機械フィールドだけを書きます。その後のLLM処理は
2フェーズに分かれます。まず全feedstockのsummaryを、対象ターン自身のユーザー入力とAgent回答だけから
並列作成します。全summaryの完了後、対象ユーザー入力と過去ターンだけを渡してAssertion集合を並列抽出します。
Assertion抽出には対象ターンのAgent回答、生成済みsummary、未来ターンを渡しません。最初の過去範囲で
参照を解決できない場合だけ、より広い上限付きの過去範囲を1回取得します。会話原文はソースログにだけ
残します。Agent回答は、後のユーザーターンが明示的に採用・訂正・却下した場合だけAssertionを成立させます。
分類Agentはschemaで検証できる構造化結果だけを返し、Feedstockを書きません。draw本体が結果を検証して
Storeの状態遷移を実行します。

各Assertionはtypeを必ず1つ、subject文字列を明示的に1つ、自己完結した主張を持ち、理由とtriggerは
任意です。空のsubject文字列は、現在のsubjectマスタに一致先がないことを表します。Drawは原子的な意味ごとに全subjectマスタと照合し、複数に一致すればsubjectごとに
Assertionを1件ずつ作ります。一致しなければ、subjectなしKnowledgeを作らず、後から分類し直せる
subjectなしAssertionを1件残します。feedstockの `types` と `subjects` はAssertionから機械導出されるため、Assertionが
空のfeedstockはBrewで効率よく機械スキップでき、本文と独立した分類が食い違うこともありません。

**`brew`** はその証拠を読み、*knowledge* を書きます。元のターンを越えて役立ち、後から
独立して訂正・置換・蒸留できる、確定した意味上の主張です。subject付きAssertionをソース時系列順に
1件ずつ原文と照合し、同じsubjectのKnowledge一覧から関係しそうなものを全文で読み、
意味的関係 `equivalent` / `complements` / `conflicts` だけを構造化結果として返します。意味上の資格はDrawとBrewの
どちらでもtypeマスタの定義だけを正本とし、別のハードコードされたカテゴリ除外規則は重ねません。

ここでいう1主張は「1文で書く」という意味ではなく、独立して変更できる単位です。Aだけを
後から訂正・無効化してもBが成り立つなら、AとBは別レコードにします。一方、Aの真偽を決める
条件・適用範囲・例外はAのClaimに残します。

**承認するのはあなた** — 新しいknowledgeの `approved` は未チェックです。Obsidianで
チェックするか、Markdownを `approved: true` に変えて初めて、エージェントから見える
ようになります。

## 使い始める

knowledgeファイルを置きたいディレクトリ(Obsidian Vault内のフォルダが好適)で
`init` を実行します。

```sh
knowbrew init
```

どのセッションログを読むか、どのLLMバックエンドを使うか、Claude Code / Codex に
登録するかを対話で聞きます。再度 `init` した場合は既存値を初期値として表示し、対話で
聞かない設定と独自ソースを保持します。新しく追加されたキーが欠けている場合だけ既定値を
補完します。

あとは知識ベースを育てるだけです。

```sh
knowbrew draw    # セッションログ → feedstock
knowbrew brew    # feedstock → pending の knowledge
```

どちらも冪等で、何度実行しても安全です。パスもフラグも指定しない `draw` は、過去24時間に
更新された設定済みセッションファイルを毎回パースし、実行中の末尾ターンを除外して、決定的なfeedstock IDによって未取得分だけを
書き、今回選ばれたセッション内でsummaryが空のものだけを要約し、summary済みかつ未分類のものだけから
Assertionを抽出します。取得カーソルやセッション別stateは
保持しません。複数の `draw` が同時に起動した場合は互いに待って同じ範囲を再確認するため、
hookが重なっても取得・分類は重複しません。hookからでも、cronからでも、手動でも構いません。

`brew` はFeedstockにすでに保存されたAssertionを処理し、別の候補ファイルや候補抽出フェーズを
持ちません。原文検証の結果は、維持・IDとsubjectを保った訂正・却下による削除のいずれかです。
解決したAssertion IDは `brewed_assertions` に記録します。中断時にKnowledgeだけが先に書けていても、
KnowledgeのAssertion参照からマーカーを復旧し、次回は未解決のsubject付きAssertionだけを続行します。
subjectなしAssertionは未醸造のまま残るため、後からsubjectを付ければ対象になります。

実行中の進捗行には、累積の入力・出力トークンが `in ... / out ...` と分けて表示されます。
完了時のJSONには、backend、model、通常入力、cache read入力、cache write入力、出力、合計を
含む `usage` オブジェクトが入ります。これらにプロバイダの最新単価を掛ければAPI料金を
計算できます。単価はCLIと独立して変わるためknowbrewには内蔵せず、バックエンドが返さない
トークン区分は0として返します。

できたものを確認し、エージェントに使わせたいものを昇格させます。

```yaml
# knowledge/kn-8f17c9a6b4d2e301.md
approved: true   # 元は false
```

`trigger: always` が付いたレコードは、`init` が登録するSessionStart hookによって
毎セッションの冒頭に注入されます。それ以外はエージェントが検索して見つけます。

## 日々の使い方

knowledgeを検索する:

```sh
knowbrew knowledge -- sqlite locking
knowbrew knowledge --subject myproject --type decision
```

実際に何が起きたかを遡る:

```sh
knowbrew feedstock --last 10                      # 直近10ターン
knowbrew feedstock --subject myproject -- deploy  # デプロイを触ったのはいつ?
knowbrew show <feedstock-id> --raw                # その時の会話そのもの
```

キーワードは `--` の後に置きます。キーワードがあれば関連度順、なければ新しい順に
なるので、`feedstock` はそのまま「何をやっていたか」の時系列として読めます。
subjectは具体的な対象を表し、対象横断の主題は独立した統制語彙ではなく全文検索で探します。
feedstockの全文検索対象はsummaryとAssertionsです。未加工の会話が必要な場合は、ソースログを
読む `show --raw` を使います。

エージェントも同じコマンドを使います。`init` が `CLAUDE.md` / `AGENTS.md` に
使い方の指示を書き込むので、いつ使えばよいかをエージェントが知っています。

## knowledgeのtype

knowledgeレコードは必ず1つのtypeを持ちます。typeは `masters/types/` 配下の
マスタノートで管理され、ファイル名が `--type` に渡す値になります。各ノートの
`definition` と任意の `example` は分類・醸造エージェントへ渡されます。`init` は
次の7件を既定として作成します。

| type | 中身 |
|---|---|
| `definition` | 用語・概念について確定した意味または境界 |
| `property` | 一つの対象の永続的な属性・能力。暫定設定、実行結果、作業状態は除く |
| `relation` | 対象または概念の間で確定した関係 |
| `principle` | 確立された一般的な原因・仕組み・反復傾向 |
| `constraint` | 外部から課される、確定した限界または必須条件 |
| `decision` | 現在の作業を越えて適用する確定済みの選択。暫定・一度限りの調整は除く |
| `preference` | 一時的な依頼や拘束的な決定ではない、継続的な選好 |

typeはフィルタとして便利ですが(`--type decision` で決定だけを見返す)、醸造を
厳しく保つ役割もあります。原子化より先に知識資格を判定し、意味がいずれか1つのtypeへ
正確に当てはまるAssertionだけを書きます。feedbackのような取得経路や、Wiki・Runbookのような
最終文書形式はtypeではありません。マスタの定義文の編集、typeの追加、不要なtypeの削除は
自由です。既定7件を再生成するのは `masters/types/` にtypeノートが1件もない場合だけで、
1件でも残っていればknowbrewはその構成に触れません。

## subject

subjectは安定した対象名で、`masters/subjects/` 配下のマスタノートとして管理します。
knowbrewがsubjectマスタを自動追加するのは、Gitリポジトリから機械的に導出できる場合
だけです。リポジトリのremoteと作業ディレクトリは、そのマスタのaliasとして記録します。
この機械処理はマスタの準備だけを行い、Feedstockへsubjectを自動付与しません。Drawは原文で
明示された対象を優先し、対象が省略されていて作業対象のシステムを指す場合に限ってrepoを
暗黙の対象として使います。

subjectノートには `definition` / `includes` / `excludes` を任意で書けます。DrawとBrewはこれらを
subject境界の正本として使い、どれもなければ名前だけを手掛かりにします。`excludes` は名前一致より
優先します。`aliases` は機械照合専用で、意味判断の材料としてLLMには渡しません。

Drawは既存のsubjectマスタだけに一致させるかsubjectを省略でき、BrewはAssertionのsubjectを
維持します。どちらも新規作成はできません。未知の `--subject` はエラーになり、
`--new-subject` フラグはありません。Vault上でsubjectマスタの作成・リネーム・統合・削除を
手動で行うことは引き続き可能です。

## 設定

`init` が `<root>/.knowbrew/config.toml` を書きます。

```toml
root = ".."

[llm]
backend = "claude-cli"    # codex-cli / api / ollama も可
draw_model = ""           # ターンごとの分類。速いモデルが向く
brew_model = ""           # 知識化の判断。質の良いモデルが向く
draw_effort = "low"        # 反復分類。initの既定値はlow
brew_effort = ""           # 空ならバックエンドまたはユーザー設定の既定
timeout = "5m"

[draw]
concurrency = 5           # 各LLMフェーズで使う並列数
context_turns = 3         # Assertion抽出へ渡す過去の対話ターン数
max_context_turns = 20    # 文脈不足時に1回だけ取得できる過去ターン上限

[[sources]]
agent = "claude"
path = "/Users/example/.claude/projects"
parser = "claude"
```

モデルが空ならCLIバックエンドの既定モデルを使います。`api` と `ollama` は両方の
モデル指定が必須で、認証情報は環境変数から読みます。

```sh
export KNOWBREW_API_URL=https://api.example.com/v1/chat/completions
export KNOWBREW_API_KEY=...
```

APIキーは環境変数からのみ読み、knowledgeルートには決して書きません。ルートの位置は
`~/.config/knowbrew/location.toml` に記録されるので、どのディレクトリからでも
使えます。

CLIバックエンドはあなた自身のエージェントを起動するため、`CLAUDE.md` /
`AGENTS.md` が適用されます。knowbrewが書く内容は、書く言語も含めてあなたの指示に
従います。この裏方の実行ではMCPサーバーは読み込まれず、knowbrew自身の
SessionStart hookも発火しません。

## 必要環境

- LLMバックエンドが1つ: Claude Code CLI / Codex CLI / tool calling対応の
  OpenAI互換エンドポイント / tool対応モデルのOllama
- ソースからビルドする場合のみGo 1.25以上(SQLite FTS5はpure Go実装のため
  CGOは不要)

他のインストール方法:

```sh
go install github.com/siro33950/knowbrew/cmd/knowbrew@latest
```

ビルド済みバイナリは
[GitHub Releases](https://github.com/siro33950/knowbrew/releases)にあります。

## コマンド一覧

```text
knowbrew init                     対話セットアップ
knowbrew draw [flags] [path...]      セッションログ → feedstock
knowbrew brew [--verbose]            feedstock → pending の knowledge
knowbrew knowledge [keywords...]  knowledgeの検索(別名: kn)
knowbrew knowledge show <id...>  lifecycle状態を問わずknowledgeを表示
knowbrew feedstock [keywords...]  feedstockの検索・振り返り
knowbrew show <id...>             feedstock 1件。--raw で会話原文
```

検索の共通フラグ: `--subject` / `--type` / `--since` / `--until` /
`--limit` / `--max-tokens` / `--reindex`。`knowledge` には `--include-pending` /
`--include-retired` / `--trigger always`、`feedstock` には `--session` /
`--agent` / `--last N` があります。

引数なしの `draw` は、過去24時間に更新された設定済みセッションファイルをスキャンします。
更新時刻はファイル選別にだけ使い、選ばれたセッションは前後文脈を保つため全体をパースします。
期間は `--since 6h` / `--since 7d` / RFC3339日時・日付で変更でき、反対側の境界は
`--until`、ソースは `--source claude` / `--source codex` で限定できます。設定済みの
全履歴を読むには明示的な `--all` が必要です。ファイルまたはディレクトリをパス指定した場合は、
`--since` / `--until` も指定しない限り期間制限をかけません。

取得IDはagent・session ID・turn IDから決定するため、重なる期間を再パースしても、ログを移動しても
重複しません。要約・Assertion抽出の対象は今回選択されたセッション内の未完了feedstockだけであり、
無関係な過去の未完了分を巻き込みません。

LLMバックエンドが使えるのは読み取り用の `feedstock context` / `knowledge catalog` /
`knowledge show` の3操作だけです。Summary・Assertion・Brewの各Agentは、対象Feedstock IDやAssertion IDを
含まないschema検証可能な構造化結果を返します。対象を保持するdrawまたはbrew本体が結果を検証し、
Storeへ1回だけ書きます。同時にFeedstockの `types` と `subjects` の導出、決定的なAssertion IDの付与、
Feedstock本文の描画を行います。Brewは1回の起動でAssertionを1件扱います。一覧は候補発見専用で、関係対象は
同じ起動内で必ず全文を読んでから構造化判断を返さなければなりません。親プロセスがロック下で最新ファイルを読み直し、
根拠時刻の比較、Knowledge IDとファイル名の生成、根拠追記・pending直接改稿・pending後継作成・
統合・新規作成を機械的に振り分けます。

意味上の新旧判定にはMarkdownのmtimeではなく、全根拠feedstockのうち最新の時刻を使います。
古いAssertionは同値な版へ根拠を追加できますが、より新しいKnowledgeと統合・競合・置換は
できません。Knowledge検索はこの時刻を
`established_at` として返します。

承認はCLIイベントに依存しません。検索・hook・brewの次回実行時に、Obsidian上で直接
変更された `approved` を検出します。承認済みKnowledgeに `supersedes` があれば、
置換元へ `superseded_by` を永続化して通常検索から外しますが、ファイルは残します。
pendingの後継は、人間が承認するまでactiveの置換元を退役させません。置換元もpendingなら
即座に退役できます。旧形式の `status` を持つ既存レコードも読み取れ、一括書き換えは
行いません。

## 設計上の保証

- セッションログは読み取り専用。変更も複製もしません。
- feedstockの機械フィールド・summary・分類日時は不変です。Brewは原文確認後に限り
  Assertionを訂正・却下でき、その場合も `types` と `subjects` は機械的に再計算されます。
- pendingのknowledgeは人間の承認前なら直接改稿されることがあります。activeは上書きせず、
  置換案をpendingの後継として作り、承認後に置換元を `superseded` として両ファイルを残します。
- どのknowledgeも本文と独立した安定IDをファイル名として持ち、根拠Feedstockと正確な
  Assertion見出しの両方をObsidian wikilinkで参照します。
- 取り出しは常にJSONを返し、記憶された内容とエージェントへの指示を構造的に
  分離します。
- ロック・検索索引・invocation claimは `<root>/.knowbrew/state/` に置きます。
  正本はfeedstockとknowledgeです。

信頼モデルは [SECURITY.md](SECURITY.md) を参照してください。

## 開発

```sh
go test ./...
go vet ./...
gofmt -l .
```

実ログに対するパーサー検証(ログを複製せずに実行):

```sh
KNOWBREW_TEST_CLAUDE_LOG=/path/to/session.jsonl \
KNOWBREW_TEST_CODEX_LOG=/path/to/rollout.jsonl \
go test -v ./internal/parser -run TestRealLogWhenConfigured
```

リリースはバージョンタグのpushで行います。通常のpushではテストのみが走ります。

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
```

## ライセンス

MIT
