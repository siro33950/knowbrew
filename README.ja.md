# knowbrew

`knowbrew` は Claude Code / Codex のセッションログから、長く再利用できる
knowledgeを醸すローカル完結のCLIです。

データを3層に分けます。

1. 元のセッションログは全量・不変の正本として残します。
2. `knowbrew draw` はユーザーとの1ターンにつき1件、ユーザー発言原文、
   機械取得メタデータ、閉じたLLM分類を持つ不変のfeedstockを作ります。
3. `knowbrew brew` は1件1主張のknowledgeを `status: pending` で作ります。
   `active` に昇格できるのは人間だけです。

検索と原文取得は常にJSONを返し、信頼できない記憶内容とエージェントへの
指示を構造的に分離します。

[English README](README.md)

## 必要環境

- ソースからビルドする場合はGo 1.25以上
- 次のいずれかのLLMバックエンド
  - Claude Code CLI
  - Codex CLI
  - tool calling対応のOpenAI互換Chat Completions API
  - tool対応モデルを使うOllama

SQLite FTS5にはpure Goドライバを使用するためCGOは不要です。

## インストール

Goツールチェーンなしで利用できるnpm版を、Claude Code / Codexユーザー向けの
標準インストール方法とします。

```sh
npm install -g knowbrew
```

npmのランチャーは同梱されたネイティブバイナリを起動します。そのため
`knowbrew init` がhookへ記録するのは実バイナリの絶対パスです。npmの
グローバルパッケージパスはバージョン番号を含まないため、通常の更新後も
hookはそのまま利用できます。

Go 1.25以上がある場合:

```sh
go install github.com/siro33950/knowbrew/cmd/knowbrew@latest
```

ビルド済みアーカイブとchecksumは
[GitHub Releases](https://github.com/siro33950/knowbrew/releases)からも
取得できます。

チェックアウトからビルドする場合:

```sh
go build -o knowbrew ./cmd/knowbrew
```

## 初期化

保存先にしたいディレクトリへ移動して実行します。

```sh
knowbrew init
```

`init` は対話専用です。検出したログソースとLLMバックエンドを選び、
Claude Code / CodexへのSessionStart hookと静的指示の登録を確認します。
既存の設定・指示内容は保持されます。

```text
<root>/
  .knowbrew/config.toml
  feedstocks/
  knowledge/
  masters/
    topics/
    subjects/
  .state/
```

`~/.config/knowbrew/location.toml` はroot内設定へのポインタです。どの
カレントディレクトリからでもCLIを呼べます。テストや一時利用では
`KNOWBREW_CONFIG` で上書きできます。

## コマンド

```text
knowbrew init
knowbrew draw [path...]
knowbrew brew
knowbrew knowledge [keywords...]
knowbrew feedstock [keywords...]
knowbrew show <feedstock-id...>
knowbrew feedstock annotate ...
knowbrew knowledge create ...
knowbrew knowledge add-source ...
knowbrew knowledge invalidate ...
```

`knowledge` は `kn` と略せます。

`feedstock annotate` とknowledge配下の3サブコマンドが、LLMバックエンド用の
検証済み書込み境界です。CLIがスキーマ、状態、語彙、根拠参照、パスを
検証してから原子的に書き込みます。

### ログを汲み取る

引数なしではinit時に選んだソースをスキャンします。

```sh
knowbrew draw
```

移動・退避されたログも明示投入できます。

```sh
knowbrew draw /backup/claude-session.jsonl /backup/codex-sessions/
```

状態キャッシュのキーはパスではなく「agent + session ID」です。ログを
移動してもfeedstockは重複しません。状態キャッシュは派生で、安定IDと
Markdownから再構築できます。

### knowledgeを醸造する

```sh
knowbrew brew
```

バックエンドへ渡すのは未処理feedstockのIDだけです。バックエンド自身が
`show` で候補を読み、`knowledge` / `feedstock` で照合文脈を探して、次の
1結果を選びます。

- pending knowledgeの作成
- 既存knowledgeへの根拠追加
- 矛盾したknowledgeの無効化
- 何もしない

新規knowledgeは必ず `pending` です。通常検索やSessionStart注入へ出す場合、
内容を確認して人間がfrontmatterを変更します。

```yaml
status: active
```

pendingからactiveへの自動昇格はありません。

### knowledgeを検索する

```sh
knowbrew knowledge -- sqlite locking
knowbrew knowledge --subject knowbrew --topic testing -- sqlite
knowbrew knowledge --include-pending -- similar claim
knowbrew kn -- create
```

既定はactiveだけです。`--include-pending` はpendingも含めますが、
invalidatedは返しません。

SessionStart hookは次を実行します。

```sh
knowbrew knowledge --trigger always
```

返却される `approved_rules` には、人間がactive化し、triggerがalwaysの
knowledgeだけが入ります。`--trigger always` と `--include-pending` は
併用できません。

### feedstockを検索して根拠を確認する

```sh
knowbrew feedstock -- sqlite locking
knowbrew feedstock --subject knowbrew --topic testing
knowbrew feedstock --session <session-id> --agent claude
knowbrew feedstock --last 10
knowbrew show claude-01234567-89ab-cdef-0123-456789abcdef-t000001
```

`--last N` は最新N件を選び、古い順から新しい順で返します。キーワードとは
併用できません。

両検索は `--subject`、`--topic`、`--since`、`--until`、
`--limit`（既定20）、`--max-tokens`（既定2000）、`--reindex` を使えます。
キーワードありはFTS5の関連度順、なしは新しい順です。検索キーワードは
常に `--` の後へ置いてください。`create` や `annotate` もサブコマンドと
衝突せず検索できます。

全検索語が3 Unicodeコードポイント以上の場合はFTS5の関連度順になります。
3コードポイント未満の語が1つでも含まれる場合は、部分一致へフォールバックし、
新しい順・score 0で返します。

knowledgeヒットはslug、claim、`applies_when`、Markdownパスを返します。
feedstockヒットはID、時刻、summary、subjects、topicsを返します。
キーワード検索にはscoreが付き、全件数、返却件数、打切りの有無も返します。

## LLMバックエンド

設定例:

```toml
root = ".."

[llm]
backend = "claude-cli"
model = ""
timeout = "5m"

[[sources]]
agent = "claude"
path = "/Users/example/.claude/projects"
parser = "claude"
```

backendは `claude-cli` / `codex-cli` / `api` / `ollama` です。
`timeout` は `30s` / `5m` / `1h` のようなGo duration形式で、既定は5分です。

`api` では次を使います。APIキーは設定やVaultへ保存しません。

```sh
export KNOWBREW_API_URL=https://api.example.com/v1/chat/completions
export KNOWBREW_API_KEY=...
```

`ollama` の `OLLAMA_HOST` 既定値は `http://127.0.0.1:11434` です。

API / Ollamaのtool callもCLIと同じ読み書き操作へ対応します。通常のモデル
テキストをレコードとしてパースしません。brew中の読み取りは1呼出し1書込みの
claimを消費せず、成功するknowledge変更は1呼出しにつき最大1件です。

## 索引

`.state/index.sqlite` は正本から再生成できる派生索引です。

- feedstockは未登録IDだけを増分追加し、既存分を再パースしません。
- knowledgeはmtimeが変わったファイルだけ更新し、削除ファイルを索引から
  除きます。
- `PRAGMA user_version` で索引スキーマを管理します。
- バージョン不一致、破損検出、`--reindex` では全再構築します。

索引を削除してもfeedstockやknowledgeは削除されません。

## 主要な不変条件

- 生ログを変更・複製しません。
- feedstockは追記のみで、後から追加できるのは一度限りの `brewed_at` です。
- `user_quote` は人間のユーザー発言だけで、AI応答とtool出力を含みません。
- invalidated knowledgeも根拠追跡のため残します。
- 全knowledgeに存在するfeedstock IDのsourceが必要です。
- 未知topic / subjectはCLIだけがpendingでマスタへ追加します。
- ファイル名・ID・状態・参照を検証し、書込みは原子的に行います。
- JSON文字列により取得データと指示を分離します。

詳細は [SECURITY.md](SECURITY.md) を参照してください。

## リリース

メンテナはバージョンタグをpushすると、npm、GitHub Releases、
`go install` 向けのバージョンを一括で配布できます。

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
```

通常のブランチpushとpull requestではテストだけを実行し、配布しません。

## 開発

```sh
go test ./...
go vet ./...
gofmt -l .
```

実ログをコピーせずパーサーを追加検証する場合:

```sh
KNOWBREW_TEST_CLAUDE_LOG=/path/to/session.jsonl \
KNOWBREW_TEST_CODEX_LOG=/path/to/rollout.jsonl \
go test -v ./internal/parser -run TestRealLogWhenConfigured
```

## ライセンス

MIT
