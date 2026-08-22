# Design

## The actual design

### Architecture

#### DrawをFeedstock単位の二段処理として所有する

`internal/application/draw` が、Feedstockの要約・Type候補を確定する前段と、対話本文および前後文脈から未整理Knowledgeを作る後段を、一つのDrawユースケースとして所有する。`internal/application/draw/draw.go` は選択した各Feedstockを既存の並列度でワーカーへ渡し、ワーカーはFeedstock単位のclaimを保持したまま、保存済み状態を再読して未完了の段だけを実行する。前段済み・後段未完了のFeedstockは、再実行時に後段から再開する。

後段のプロンプト構築と結果反映は新設する `internal/application/draw/extract.go` が所有する。現行 `internal/application/brew/brew.go` にある対話本文、`context_before`、`context_after`、Subject master、Knowledge type masterの読み込みをここへ移す一方、KnowledgeカタログはDrawのportへ追加しない。後段は関係判定を含まないKnowledge draftの配列を構造化出力として受け取り、Domain検証後に保存する。1 Feedstockあたりの上限は `domain.MaxKnowledgePerFeedstock` の16件を適用する。前段でType候補が空になったFeedstockは後段のLLMを呼ばず、0件の抽出として後段完了を記録する。

LLM adapterには後段専用のagent taskを追加し、既存のBrew model／effortを使用する。既存の `draw` taskは低effortの前段に残し、`knowbrew feedstock context` の公開を維持する。後段taskとSubject Brew taskには、catalog、show、submitに加えて `knowbrew feedstock context` も公開せず、knowbrew CLI toolを一つも公開しない。後段は対話本文と `context_before` / `context_after` をapplicationがpromptへ載せるため同toolを必要としない。Brew taskについては、agentがservice portではなくサブプロセスのCLIからストアへ到達するため、Brew serviceからDialogueReaderを除いてもセッションログへの経路は塞がらず、taskへ同toolを公開しないことがR-012を成立させる条件である。根拠は、model／effortの選択とtool権限をtaskで分離している `internal/application/agent/agent.go` と `internal/adapters/llm/runner.go` の `claudeAllowedTools`、および外部入力であるLLM出力をDomain検証後にだけ保存する `docs/Architecture.md` の境界規則である。

#### BrewをSubject単位の意味整理として所有する

`internal/application/brew` はFeedstock処理をやめ、Subjectごとの未整理Knowledge集合と整理済みKnowledge head全体を入力にする。`internal/application/brew/brew.go` は、Subjectを持つ未整理KnowledgeをSubject別にまとめ、複数Subjectをワーカープールで並行処理する。

新設する `internal/application/brew/organize.go` が、Subject snapshotの構築、Brew用構造化出力の検証、Domainの整理結果の反映を所有する。snapshotには対象Subjectの未整理Knowledgeをすべて含め、整理済みのpending／activeなKnowledge headも検索や件数上限で切らずにすべて含める。Brew serviceからDialogueReaderを除去し、`internal/adapters/cli/root.go` でもBrewへsource gatewayを接続しない。

Brew agentはtoolを呼ばず、一つのSubjectについて各未整理Knowledge IDの処置を直接返す。処置は破棄、new、equivalent、complements、conflictsのいずれかとし、関係を持つ処置はsnapshot内の対象IDを参照する。complementsは完成した統合draftも返す。全入力IDに重複なく一つの処置があること、関係先が同じSubjectの現行headまたは時系列上それ以前に整理される入力であることをDomain適用前に検証する。

Domainは未整理Knowledgeをsource Feedstockの `CompareFeedstocks` 順に処理する。newでは入力Knowledge自身を整理済みにし、equivalentでは関係先へFeedstock根拠を統合して入力を消費し、complements／新しいconflictsでは入力KnowledgeのIDを統合後または後継Knowledgeに引き継ぐ。古いconflictsは現行どおり整理結果を変更せず入力を消費する。破棄も入力を消費する。Subject全件を一回のDomain解決へ渡して最終graphを検証するため、入力順やSubject間の完了順によって結果を変えない。

#### claimと原子的反映の責務を分離する

DrawとBrewのapplication portに、安定したキーに対するキャンセル可能な排他claimを提供する `Claimer` を置く。adapterは `internal/adapters/runlock` のfile lockを、DrawではFeedstock ID、BrewではSubject名でkeyed lockとして実装する。claimはLLM実行を含む対象単位の処理中だけ保持され、プロセス終了時にはOSによって解放される。現行の実行全体を覆う `draw.lock` と `brew.lock` は使用しない。

Draw後段では、未整理Knowledge群の作成とFeedstockの後段完了を一つのmarkdown transactionで反映する。Brewでは、一つのSubjectについて、整理済みKnowledgeの作成・更新、消費した未整理Knowledgeの削除を一つのtransactionで反映する。LLM失敗、検証失敗、commit失敗では進捗もKnowledgeも部分反映しない。根拠は、atomic operationの範囲をApplicationが定義しadapterが実装する `docs/Architecture.md` と、journal recoveryを備える `internal/adapters/persistence/markdownstore/transaction.go` である。

#### 主な変更対象

- `internal/domain/domain.go`、`internal/domain/feedstock.go`、`internal/domain/knowledge.go`: 抽出進捗、整理進捗、Subject不在、未整理／整理済みのgraph規則、およびSubject単位の決定的な整理を所有する。
- `internal/application/draw/draw.go`、`internal/application/draw/extract.go`: 二段Draw、最新ターン除外、Feedstock claim、後段promptと原子的反映を所有する。
- `internal/application/brew/brew.go`、`internal/application/brew/organize.go`: Subject選択、Subject並列実行、全headを使うprompt、整理planの反映、変更Subjectの集計を所有する。
- `internal/application/agent`、`internal/adapters/llm`: Draw後段とSubject Brewの構造化入出力およびtaskごとのmodel／tool境界を所有する。
- `internal/application/storage/storage.go`、`internal/adapters/persistence/markdown.go`、`internal/adapters/persistence/markdownstore`: 未整理Knowledgeと進捗の永続化、Knowledge削除を含むtransaction、keyed claimの格納先を所有する。
- `internal/adapters/query`: 未整理Knowledgeを検索indexへ入れない境界を所有する。
- `internal/adapters/cli/root.go`: hook modeの伝達、全体lockを使わないservice wiring、Subject単位のBrew結果表示を所有する。

### Interface

`knowbrew draw` は一回の実行内で前段と後段を行う。`--max` は前段または後段のいずれかが未完了のターン数を制限する。通常実行では選択した最新ターンも処理できる。

`knowbrew draw --hook` はhookであることをDraw optionsへ明示し、対象transcript内でDrawが完了していないターンをsource順に確定した後、その最新1件を選択対象から外す。対象が1件だけなら処理件数は0件になる。`runlock.ErrBusy` を正常終了へ変換するCLI分岐は廃止し、同一Feedstockのclaim待ちまたはclaim取得後の完了状態再読によって二重処理を防ぐ。

`knowbrew brew` は処理単位をFeedstockからSubjectへ変更する。`--max N` は未整理Knowledgeを持つSubjectを最大N件選択する。実行結果は、Knowledgeに変更が生じたSubject名の `changed_subjects` を返す。

`knowbrew knowledge` は整理済みKnowledgeだけを検索対象とする。`--include-pending` は既存どおり整理済みKnowledgeのpending lifecycleを追加表示する指定であり、未整理Knowledgeを表示する指定にはしない。`knowbrew feedstock` と `knowbrew show <feedstock-id>` の検索条件、Feedstock ID、表示対象は変更しない。

内部境界の `Claimer` は、Applicationが指定したFeedstock IDまたはSubject名について、処理終了まで一つの実行だけをownerにする。永続化transactionは、Knowledgeのstageに加えて未整理Knowledgeの削除とFeedstockの抽出完了を同じcommitへ含められる責務を持つ。

### Data Model

Feedstockのidentityは現行どおりFeedstock IDとする。`BrewedAt` は再利用せず、後段抽出の完了時刻を表す `ExtractedAt`（frontmatterは `extracted_at`）へ置き換える。`AnnotatedAt` は前段完了、`ExtractedAt` は後段完了を表し、`ExtractedAt` があるFeedstockはDraw再実行の対象外になる。A-001により既存データは残らないため、`brewed_at` からの移行表現は保持しない。

Knowledgeのidentityは現行どおりKnowledge IDとし、既存lifecycleとは直交する整理完了時刻 `OrganizedAt`（frontmatterは `organized_at`）を追加する。Draw後段が作るKnowledgeは `OrganizedAt` を持たず、`approved: false` で、supersedes関係を持たない。Brewで保持すると決まったKnowledgeだけが `OrganizedAt` を得て、そこから既存のpending／active／superseded／invalidated lifecycleへ参加する。Brewが破棄または他のKnowledgeへ統合した未整理Knowledgeは、同じSubject transactionで削除する。整理済みKnowledgeを未整理へ戻す遷移は作らない。

Subject不在はKnowledgeの `Subject` が空であること、すなわちfrontmatterに `subject` が無いこととして表現する。未整理Knowledgeに限ってSubject不在を許可し、予約Subjectやmaster entryは作らない。Subject不在KnowledgeはBrewで消費されないため、件数上限や自動削除を持たず、未整理のまま保持する。

Knowledge headの選択は「全整理済みhead」と「非空のSubjectに完全一致する整理済みhead」の操作に分ける。空文字を「絞り込みなし」と解釈する現行の多義的な契約は廃止し、Subject不在と絞り込みなしを同じ値で表現しない。graphの同一Subject・同一statement重複検査は整理済みheadだけへ適用し、未整理Knowledge同士には適用しない。

### Database

永続的な正本は引き続きmarkdown storeであり、新しいdatabase tableは追加しない。FeedstockとKnowledgeの新しい進捗は各frontmatterに保持し、schema versionを更新する。A-001に基づきmigration処理は作らない。

SQLite検索indexの同期時に `OrganizedAt` の無いKnowledgeをindex対象から除外する。以前index済みのpathが未整理として読まれた場合にも対応するrecordを削除する。これにより通常検索、Subject絞り込み、`--include-pending` のすべてで未整理Knowledgeを到達不能にし、整理完了後は通常のmtime差分同期でindexへ追加する。Subject不在Knowledge用のaccess pathや件数管理tableは追加しない。

### UI/UX

CLIのみが対象であり、新しい画面は追加しない。Brew完了時の `changed_subjects` を、利用者が承認を開始するSubjectの識別子として提示する。

### Algorithm

Drawの対象選択では「Feedstockが無い」「前段未完了」「後段未完了」をいずれも未完了ターンとして扱う。hook modeだけは、対象transcriptの未完了ターンをsource内の順序で並べた後に末尾1件を除外し、その後に通常の選択処理へ渡す。各ワーカーはFeedstock claim取得後に状態を再読し、前段結果は単独でcommitし、続けて後段を実行する。後段結果は、全draftの検証、最大16件の確認、Knowledge群の作成、`ExtractedAt` の設定を一つのtransactionで行う。

BrewはSubject claim取得後に、そのSubjectについて `OrganizedAt` の無い入力集合と、`OrganizedAt` のあるpending／active head全体をsnapshot化する。未整理入力は `CompareFeedstocks` で時系列順にし、同一時刻は既存のsession ID、turn ID、Feedstock IDの比較で決定する。headはID順にしてpromptを決定的にする。検索indexやセッションログはこの処理で参照しない。

agentのplanはsnapshot内の未整理IDを一度ずつ処置し、Domain resolverも同じ時系列順で適用する。conflictsの新旧は `CompareFeedstocks` で決め、complementsはplanが返した完全な統合draftを使う。全処置後に、既存のsupersession cycle、current successor、Subject一致の検証に加え、整理済みheadだけのstatement重複を検証する。Subject単位のtransactionが成功した時だけ入力を消費するため、同じ入力で再実行しても変更は生じず、処理中にDrawが追加したsnapshot外のKnowledgeは次回入力として残る。

Behaviorの検証は、Domainの状態遷移と順序独立性をunit test、Draw/Brewのclaim・中断再開・transaction境界をapplicationおよびpersistence test、hook選択・検索非表示・Feedstock互換・変更Subject出力をCLI／query integration test、生ログ削除後のBrewからDistillまでをend-to-end testで確認する。LLMを使う受入条件は固定した構造化応答を返すrunnerで、promptに全Subject headが含まれること、およびBrewにDialogueReader呼び出しが無くBrew taskへ公開するtool集合が空であることを検証する。

### Infra

外部サービスやdeploy構成の変更はない。`.knowbrew/state` 配下にFeedstock ID用とSubject名用のkeyed lockを置く。lock fileはclaimのidentityだけを表し、処理進捗や回収対象を保持しない。実際の進捗はFeedstock／Knowledgeの正本にだけ保持する。

## Alternatives Considered

- `BrewedAt` を抽出完了として読み替える案は採らない。Brewの責務がSubject側へ移った後も名称が旧責務を示し、Feedstock進捗とKnowledge進捗のownerを誤らせるためである。A-001により互換読込が不要なので `ExtractedAt` へ改名する。
- Draw／Brewの実行全体をglobal lockで直列化する案は採らない。異なるFeedstockまたはSubjectまで待たせ、R-002、R-005、R-014を満たせないため、Feedstock／Subject単位のclaimにする。
- Feedstockごとのcatalog digest、catalog／show／submit、stale retryをBrewに残す案は採らない。Subject claim下で全headを一つのsnapshotとして解決する設計と責務が重複し、検索候補上限も残るためである。
- Subject不在を予約Subject名で表す案は採らない。master、Brew、Distill、SessionStartのすべてで特別扱いが必要になり、Subject指定操作から自然に除外するR-009を満たさないためである。

## Cross-cutting concerns

並行性と再開可能性は、長時間のLLM実行を覆うkeyed claimと、短時間の永続化commitを分けて保証する。同じキーでは再読後に完了済み処理をskipし、異なるキーではLLM実行を並行できる。commitはDraw後段またはSubject Brewの成果を一括反映し、journal recovery後も完了markerと成果物が食い違わないようにする。

検索、Distill、承認対象の境界はすべて `OrganizedAt` を共通判定にする。未整理という状態を既存のpending lifecycleへ混ぜないことで、`approved`、supersession、invalidation、Distill入力、SessionStart文書組み立ての既存規則を変更しない。

## Risks

- 一つのSubjectのsnapshotが単一promptに収まる前提が未確定である。Brewは対象Subjectの未整理Knowledge全件と整理済みhead全体を一回のLLM呼び出しへ載せるが、`--max N` はSubject件数を絞るだけで1 Subjectあたりの規模を抑えない。Subjectあたりのhead数と未整理件数がどこまで伸びるかは実装開始時点で未確定であり、model入力上限を超えるとheadを削るほかなくなり、R-013とB-020を満たせない。
