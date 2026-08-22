# Context

## 入力

- Issue #15「Draw後段でKnowledgeを作成し、BrewをSubject単位の意味整理に変更する」
  <https://github.com/siro33950/knowbrew/issues/15>（本要求の唯一の正本）
- Issueが根拠として引用する現行実装
  - `internal/application/brew/brew.go`
  - `internal/application/brew/submit.go`
  - `internal/application/draw/draw.go`
  - `internal/domain/knowledge.go`
  - `internal/domain/feedstock.go`
  - `internal/adapters/dialogue/query.go`
  - `internal/adapters/cli/root.go`
  - `internal/adapters/source/cache.go`
- `docs/Architecture.md`（依存方向と層責務。本変更でも従う）

## 現行パイプライン

knowbrewは、セッションログを段階的に知識へ変換するCLIである。

```text
セッションログ ─draw─▶ feedstock ─brew─▶ knowledge ─承認─▶ distill ─▶ documents
```

| 段 | 入力 | 出力 | 生ログ | カタログ |
|---|---|---|---|---|
| Draw | 1ターン + prior_turns | Feedstock (summary, types) | 読む | — |
| Brew | Feedstock (types非空) | Knowledge | 読み直す | Subject指定の検索結果 |
| 承認 | Knowledge | active | — | — |
| Distill | active な Knowledge | 文書 | — | — |

承認は、Knowledgeファイルのフロントマターを `approved: true` にすることで行う。knowbrewが作成するKnowledgeは `approved: false` から始まり、既定の検索には出ない。

## 確定済みの制約

- 関係判定は既にSubject内で閉じている。関係先はSubject一致が必須であり（`internal/application/brew/submit.go:418`、`internal/domain/knowledge.go:512`）、catalogもSubject指定必須である（`internal/adapters/cli/root.go:781` 以降）。Subjectをまたぐ依存は存在しない。
- Knowledgeのstatementは元の対話なしで主題を特定できることが求められている（現行Brewプロンプトの `without the source dialogue`、`internal/application/brew/brew.go:329`）。関係判定に元の対話は不要である。
- conflictsの前後関係は処理順ではなくデータで決まる（`CompareFeedstocks(source, target.Established)`、`internal/domain/knowledge.go:139`）。
- Drawは既に並列実行される（`defaultConcurrency = 5`、`internal/application/draw/draw.go:63`）。
- 生ログ本文はknowbrew内に保存されず、参照のたびにソースログから読み直す（`internal/adapters/dialogue/query.go`）。
- ソースログのキャッシュDBはWALと `busy_timeout` を設定済みである（`internal/adapters/source/cache.go:187`）。

# Outcome

## 対象者

- knowbrewを使って自分のセッションログから知識を蓄積し、承認する利用者。
- knowbrewの実行コストと実行時間を管理する運用者。

## 現在の問題

1. **Brewが逐次実行を強制される。** カタログを読んで書き戻すため、Feedstockを時系列順に1件ずつ処理するほかない。処理量の増加が実行時間に直結する。
2. **Brewが生ログを読み直す。** Drawが読んだのと同じターンを再取得するため、ソースログが消えるとBrewが実行できなくなる。ログ消失の窓が「Brewが追いつくまで」の全期間に広がる。
3. **関係判定の視野が狭い。** 「1 Feedstock vs Subject内検索の上位30件」で判断しており、Subject全体を見ていない。検索が取りこぼした関係先を見落とす。
4. **承認の着手点が決まらない。** 大量に生成されたKnowledgeのどこから確認すればよいか判別できない。

## 変更後に実現する状態

抽出と関係判定を分離し、関係判定の単位を「1 Feedstock vs 検索結果」から「Subject全体」へ移す。

| 段 | 入力 | 出力 | 生ログ | カタログ | 並列単位 | 駆動 |
|---|---|---|---|---|---|---|
| Draw前段 | 1ターン | Feedstock (summary, types) | 読む | — | ターン | フック / バッチ |
| Draw後段 | Feedstock + 対話 + 前後文脈 | Knowledge（未整理） | 読む | 読まない | ターン | 同上 |
| Brew | 未整理Knowledge + 既存head | 整理済みKnowledge | 読まない | Subject全体 | Subject | 日次・差分 |
| 承認 | 変更分 | active | — | — | — | 随時 |
| Distill | active な Knowledge | 文書 | — | — | Subject×Template | 週次 |

「駆動」列の日次・週次は運用上の例示であり、実行頻度は本変更の要求ではない。要求は入力・出力・生ログ参照の有無・関係判定の視野・並列単位である。

これにより、生ログを読む段がDrawだけに確定し、Brewの視野がSubject全体に広がり、BrewがSubject単位で並列かつ差分駆動になり、承認がSubject単位でまとまる。

# Current Behavior

## Brewは1件ずつ逐次に処理する

`knowbrew brew` は、Brew未実施のFeedstockを時系列順にソートし（`internal/application/brew/brew.go:94`）、`for _, feedstock := range pending` のループで1件ずつ処理する。Draw側の `defaultConcurrency` に相当する並列度の設定はBrewに存在せず、`Options` は `Max` だけを持つ（`internal/application/brew/ports.go`）。

再現手順と観測結果:

1. Brew未実施のFeedstockが複数ある状態で `knowbrew brew --verbose` を実行する。
2. `Brewing · N/M feedstocks` の進捗表示がM件について1ずつ増える。同時に複数件が進行することはない。

さらに、Feedstockごとにカタログのdigestを用いた楽観ロックが働き、digest不一致時は `ErrStaleDecision` を返し（`internal/application/brew/submit.go:361`）、Brewは同一Feedstockを最大2回試行する（`internal/application/brew/brew.go:130`）。

## Brewが生ログを読み直す

Brewはプロンプト構築時に、対象Feedstockの対話本文と前後文脈のFeedstockの対話本文を読む（`feedstockPrompt` および `feedstockContext`、`internal/application/brew/brew.go`）。読み出しは `Query.Read` を経由し、Feedstockに記録された `agent` / `session.id` / `turn_id` からソースログを引く（`internal/adapters/dialogue/query.go`）。対話本文はknowbrew内に保存されていない。

再現手順と観測結果:

1. Draw済みかつBrew未実施のFeedstockを用意する。
2. そのFeedstockのソースログファイルを削除する。
3. `knowbrew brew` を実行すると、該当Feedstockは `summary.failures` に記録され `feedstocks_failed` が増える。以降そのFeedstockからKnowledgeは作られない。

Issue #15が報告する実測値では、`.knowbrew/state/source-index.sqlite` は119MB・9,504ファイル（生ログ約10GB相当）を保持しており、そのうち67本は実ファイルが既に存在しない。

## 関係判定の視野が検索上位30件に限られる

現行Brewプロンプトは、各意味について `knowbrew knowledge catalog --subject <subject> --query <statement>` を実行し、その結果に対してのみ関係（new / equivalent / complements / conflicts）を決めるよう指示する（`internal/application/brew/brew.go:330`）。catalogコマンドは検索候補を `Limit: 30` で取得する（`internal/adapters/cli/root.go:804`）。submit時の検証も、候補がその呼び出しのcatalogに含まれていることを要求する（`internal/application/brew/submit.go:414`）。

したがって、Subjectに31件以上のKnowledge headがある場合、検索順位31位以下のKnowledgeは関係先として選べない。

## 承認の着手点が判別できない

Brewの出力Summaryは `knowledge_created` / `knowledge_updated` / `knowledge_evidence_added` の総数とFeedstock単位の失敗一覧だけを返す（`internal/application/brew/brew.go` の `Summary`）。どのSubjectに変更が生じたかは出力されない。承認は個々のKnowledgeファイルを `approved: true` にする操作であり、Subject単位でまとまった変更分を示す出力は存在しない。

## その他、本変更に関係する現在の挙動

- **Feedstockの進捗は2値である。** `PendingBrew()` は `AnnotatedAt != nil && len(Types) > 0 && BrewedAt == nil` で判定する（`internal/domain/feedstock.go:44`）。抽出済みかどうかと整理済みかどうかを区別する表現はない。
- **フック実行はロック競合時に黙って終了する。** `knowbrew draw --hook` はDrawロックを `Immediate` で取得し（`internal/adapters/cli/root.go:165`）、`runlock.ErrBusy` を握りつぶして正常終了する（`internal/adapters/cli/root.go:171`）。そのフック起動分のターンは処理されない。
- **フック実行は最新ターンを含む。** フックは `transcript_path` のtranscriptから未Drawターンを引くため（`internal/adapters/cli/root.go:110`）、定常状態では後続ターンが存在しないターンを処理対象に含む。その結果 `context_after` が得られない。
- **同一statementのKnowledgeは書き込めない。** `ValidateKnowledgeGraph` は現行head全体について subject + 正規化statement の完全一致を拒否する（`internal/domain/knowledge.go:267`）。
- **KnowledgeはSubject必須である。** `Vocabulary.ValidateSubject` は空のSubjectを拒否し、`masters/subjects` に存在しない名前も拒否する（`internal/domain/knowledge.go:453`）。現行Brewプロンプトも、当てはまるSubjectが無い意味は登録しないよう指示する（`internal/application/brew/brew.go:328`）。
- **1 Feedstockあたりの登録上限は16件である。** `MaxKnowledgePerFeedstock = 16`（`internal/domain/knowledge.go:11`）。Brewの登録時に検証される（`internal/application/brew/submit.go:158`, `:255`）。
- **未承認Knowledgeは既定の検索に出ないが、明示すれば出る。** `knowbrew knowledge` は既定で未承認を除外し、`--include-pending` を付けると含める（`internal/adapters/cli/root.go:765`, `:775`）。
- **Drawは並列で走る。** `defaultConcurrency = 5`（`internal/application/draw/draw.go:63`）。

## 実行規模

Issue #15が報告する実測値は次のとおりである。Feedstock 12,882 / Draw済み 10,942 / types非空 1,956 / Brew済み 226 / Knowledge 479。types非空かつBrew未実施のFeedstockは1,730件であり、これが逐次Brewの待ち行列とログ消失の窓に相当する。

# Scope / Non-goals

## 変更するもの

- Drawの段構成。Feedstockを作る前段と、Feedstockから未整理Knowledgeを作る後段に分ける。
- Knowledgeを作成する担当段。Brewから Draw後段へ移す。
- Brewの責務、入力、処理単位、駆動方式。Subject単位の意味整理に変える。
- フック実行時のDrawの対象ターン選択と競合処理。
- Knowledgeの重複検査の適用範囲。
- KnowledgeにおけるSubjectの必須性。
- Feedstockおよび Knowledgeの進捗表現。
- 未整理Knowledgeの検索可視性。

## 変更しないもの

- Feedstockの存在とライフサイクル。前段と後段の間のキューとして残す。
- Knowledgeのライフサイクル状態（pending / active / superseded / invalidated）とその遷移規則。
- 承認の手段。Knowledgeファイルの `approved: true` のままとする。
- Distillの入力と出力。承認済みかつ現行のKnowledgeからSubject文書を再生成する。
- SessionStartで注入される文書の組み立て方。
- `docs/Architecture.md` の依存方向と層責務。

## 対象外（Issue #15が別issueとしたもの）

- Subject追加時に、Subject未割り当てのKnowledgeプールを掃く仕組み。
- Distillを「承認済み集合が変わったSubjectのみ」に絞る差分検知。
- Agentによる承認経路。

## 対象外（前提条件により不要）

- 既存データの移行。本変更の適用前に、`masters/subjects` と `masters/types` を除く既存データはすべて削除される（Assumptions参照）。既存Feedstock、既存Knowledge、生成済み文書、進捗状態を新しい表現へ変換する処理は作らない。
- 上記の削除を行う手段の提供。削除は人間が実施する前提条件であり、本変更の成果物に含めない。

# Requirements

- R-001: `knowbrew draw` の1回の実行で、Feedstockの作成（前段）と、そのFeedstockからの未整理Knowledgeの作成（後段）の両方が行われる。
- R-002: Draw後段は複数のターンを並行して処理する。並列度はDrawの既存の並列度設定に従う。
- R-003: Draw後段はKnowledgeカタログを読まず、作成する未整理Knowledgeについて既存Knowledgeとの関係（new / equivalent / complements / conflicts）を決めない。
- R-004: `knowbrew draw --hook` は、対象transcriptのターンのうち前段と後段の少なくとも一方が未完了であるものを処理対象の候補とし、その候補のうち最新の1ターンを処理対象から除外する。除外されたターンは、後続のフック実行またはバッチ実行で処理される。
- R-005: `knowbrew draw --hook` は、他のDraw実行と同時に起動しても処理を放棄しない。同一Feedstockに対する二重処理だけが防がれ、異なるFeedstockは並行して処理される。
- R-006: 同一Subjectかつ同一statementの未整理Knowledgeが併存しても、Draw後段の書き込みは失敗しない。
- R-007: 1つのFeedstockから作成される未整理Knowledgeの件数上限は16件とし、現行の上限を維持する。
- R-008: どのSubjectにも割り当てられない意味も、Subjectを持たないKnowledgeとして作成できる。Subject不在は予約Subject名ではなく、Subjectが無い状態として表現する。
- R-009: Subjectを持たないKnowledgeは、Subjectを指定する操作（Brewの処理対象、Distillの入力、Subject絞り込み検索、SessionStartの注入）のいずれにも現れない。`masters/subjects` にSubject不在を表す予約エントリは追加されない。
- R-010: Subjectを持たないKnowledgeの蓄積量に上限を設けない。蓄積量を理由とする作成失敗や自動破棄は起きない。
- R-011: 未整理Knowledgeは `knowbrew knowledge` の検索結果に出ない。`--include-pending` を指定した場合も出ない。
- R-012: Brewはセッションログを読まない。Brewの入力は未整理Knowledgeと、対象Subjectの既存Knowledge headだけである。
- R-013: BrewはSubject単位で処理し、対象SubjectのKnowledge head全体を関係判定の視野とする。検索結果の上位N件に限定しない。
- R-014: Brewは複数のSubjectを並行して処理できる。あるSubjectの処理結果は他のSubjectの処理結果に依存せず、処理順によって結果が変わらない。
- R-015: Brewは前回以降に増えた未整理Knowledgeだけを入力とする。同じ入力に対して2回目の実行は新たなKnowledgeの変更を生まない。
- R-016: Brewは入力された未整理Knowledgeを整理済みにせず破棄できる。破棄された未整理Knowledgeは承認対象にならず、検索にも現れない。
- R-017: Brewの実行結果から、Knowledgeに変更が生じたSubjectを識別できる。
- R-018: 処理を中断して再実行した場合、後段が完了したFeedstockと整理済みのKnowledgeは再処理されない。未完了のものだけが処理される。
- R-019: セッションログが削除されても、Draw後段が完了したFeedstock以降の段（Brew、承認、Distill）は実行できる。
- R-020: Feedstockは廃止しない。`knowbrew feedstock` の検索と `knowbrew show <feedstock-id>` は、本変更の前後で同じFeedstockを返す。
- R-021: Knowledgeのライフサイクル状態と遷移規則、承認の手段（`approved: true`）、Distillの入力（承認済みかつ現行のKnowledge）、SessionStartで注入される文書の組み立て方は、本変更の前後で変わらない。

# Assumptions / Open Questions

## Assumptions

- A-001: 本変更の適用前に、`masters/subjects` と `masters/types` を除く既存データ（既存Feedstock、既存Knowledge、生成済み文書、進捗状態）はすべて削除される。削除は人間が実施する前提条件であり、本変更のScope外である。既存データの移行対象は存在しない。（人間の明示的な確認により確定）

## Open Questions

なし。
