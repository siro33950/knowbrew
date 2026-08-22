## B-001: Drawの一回実行で前段と後段が完了する

GIVEN 未Drawで、Knowledgeとして保持する意味を含むターンが処理対象に選ばれている
WHEN 利用者が`knowbrew draw`を実行する
THEN 同じ実行内で対象ターンのFeedstockが作成される
AND そのFeedstockから未整理Knowledgeが作成される

## B-002: Draw後段がターン単位で並行する

GIVEN Draw後段の処理を待つFeedstockが複数存在する
WHEN `knowbrew draw`がDraw後段を実行する
THEN 複数Feedstockの後段処理がDrawに設定された並列度の範囲で並行して進む

## B-003: Draw後段は既存Knowledgeから独立して抽出する

GIVEN Feedstockと既存Knowledgeが存在する
WHEN Draw後段が未整理Knowledgeを作成する
THEN Knowledgeカタログの内容や利用可否にかかわらず抽出が行われる
AND 作成された未整理Knowledgeには、既存Knowledgeに対するnew、equivalent、complements、conflictsの判定結果が付与されない

## B-004: フック実行は最新の未完了ターンを除外する

GIVEN 対象transcriptに、前段と後段の少なくとも一方が未完了であるターンが一つ以上存在する
WHEN `knowbrew draw --hook`が実行される
THEN それらのターンのうち最新の一つを除くターンが処理対象になる
AND 除外されたターンでは前段も後段も実行されない

## B-005: 次のフックが前回除外したターンを処理する

GIVEN 前回のフックで最新だったため除外された、前段と後段の少なくとも一方が未完了であるターンが存在する
AND 同じtranscriptにそれより新しい未完了のターンが追加されている
WHEN `knowbrew draw --hook`が再度実行される
THEN 前回除外されたターンが処理される
AND 新たに最新となった未完了のターンが処理対象から除外される

## B-006: バッチ実行がフックで除外されたターンを処理する

GIVEN フック実行で最新だったため除外された、前段と後段の少なくとも一方が未完了であるターンが存在する
WHEN フックではない`knowbrew draw`がそのターンを対象に実行される
THEN 除外されていたターンが処理される

## B-007: 同時のDraw実行は処理対象を失わない

GIVEN 同じFeedstockと異なるFeedstockを対象に含む複数のDraw実行が同時に起動する
WHEN そのうち一つ以上が`knowbrew draw --hook`である
THEN Draw全体の競合を理由にフックの処理対象が放棄されない
AND 同じFeedstockの後段処理は一度だけ行われる
AND 異なるFeedstockは互いを待たずに処理できる

## B-008: 同一内容の未整理Knowledgeが併存できる

GIVEN 同じSubjectと同じstatementを持つ未整理Knowledgeが既に存在する
WHEN 別のFeedstockのDraw後段が同じSubjectとstatementの未整理Knowledgeを作成する
THEN 重複を理由に書き込みは失敗しない
AND 両方の未整理Knowledgeが保持される

## B-009: 一つのFeedstockから16件まで作成できる

GIVEN 一つのFeedstockから作成対象となる意味が16件抽出されている
WHEN Draw後段が完了する
THEN そのFeedstockに由来する未整理Knowledgeが16件作成される

## B-010: 一つのFeedstockから17件以上は作成されない

GIVEN 一つのFeedstockから16件を超える作成候補が生じている
WHEN Draw後段の結果が反映される
THEN そのFeedstockに由来する未整理Knowledgeの件数は16件を超えない

## B-011: SubjectなしのKnowledgeを作成できる

GIVEN 抽出された意味を割り当てられるSubjectが`masters/subjects`に存在しない
WHEN Draw後段がその意味をKnowledgeとして作成する
THEN Subjectを持たない未整理Knowledgeとして保存される
AND Subject不在を表す予約エントリは`masters/subjects`に追加されない

## B-012: SubjectなしのKnowledgeはBrew対象にならない

GIVEN Subjectを持たない未整理Knowledgeが存在する
WHEN Brewが処理対象のSubjectを選ぶ
THEN そのKnowledgeはどのSubjectの処理入力にも含まれない

## B-013: SubjectなしのKnowledgeはDistill対象にならない

GIVEN Subjectを持たないKnowledgeが存在する
WHEN DistillがSubject文書の入力を選ぶ
THEN そのKnowledgeはどのSubject文書の入力にも含まれない

## B-014: SubjectなしのKnowledgeはSubject検索に現れない

GIVEN Subjectを持たないKnowledgeが存在する
WHEN 利用者が任意のSubjectを指定してKnowledgeを検索する
THEN そのKnowledgeは検索結果に含まれない

## B-015: SubjectなしのKnowledgeはSessionStartに注入されない

GIVEN Subjectを持たないKnowledgeが存在する
WHEN SessionStart用のコンテキストが組み立てられる
THEN そのKnowledgeは注入内容に含まれない

## B-016: SubjectなしのKnowledgeを件数制限なく蓄積できる

GIVEN Subjectを持たないKnowledgeが既に蓄積されている
WHEN Draw後段がさらにSubjectを持たないKnowledgeを作成する
THEN 既存の蓄積件数だけを理由に作成は失敗しない
AND 既存または新規のKnowledgeが蓄積件数だけを理由に自動破棄されない

## B-017: 未整理Knowledgeは検索に現れない

GIVEN 未整理Knowledgeが存在する
WHEN 利用者が`--include-pending`の指定有無にかかわらず`knowbrew knowledge`で検索する
THEN その未整理Knowledgeは検索結果に含まれない

## B-018: Brewはセッションログなしで実行できる

GIVEN Draw後段が完了し、未整理Knowledgeと対象Subjectの既存Knowledge headが保存されている
AND 対応するセッションログが存在しない
WHEN BrewがそのSubjectを処理する
THEN セッションログの欠落を理由に失敗しない
AND 保存済みの未整理Knowledgeと既存Knowledge headに基づく整理結果が得られる

## B-019: Draw後段完了後の工程はセッションログに依存しない

GIVEN Draw後段が完了したFeedstockのセッションログが削除されている
WHEN そのFeedstock由来のKnowledgeについてBrew、承認、Distillが順に行われる
THEN 各工程は削除されたセッションログを必要とせず完了する
AND 承認済みかつ現行のKnowledgeに基づく文書が生成される

## B-020: BrewはSubject全体から関係先を選べる

GIVEN 対象Subjectに未整理Knowledgeと複数の既存Knowledge headが存在する
AND 未整理Knowledgeと関係するheadが検索順位や候補件数の制限では候補外になる位置にある
WHEN BrewがそのSubjectを処理する
THEN そのheadも関係判定の対象に含まれる
AND 成立する関係が整理結果に反映される

## B-021: Brewは複数Subjectを並行して処理する

GIVEN 未整理Knowledgeを持つSubjectが複数存在する
WHEN BrewがそれらのSubjectを一回の実行対象にする
THEN Subjectごとの処理が並行して進む
AND 各Subjectの整理結果はそのSubjectのKnowledgeだけに反映される

## B-022: Subjectの処理順で整理結果が変わらない

GIVEN 同じ未整理Knowledgeと既存Knowledge headの集合がSubjectごとに保存されている
WHEN Subjectの処理開始順または完了順が異なるBrew実行が行われる
THEN 各Subjectについて同じ整理結果が得られる

## B-023: Brewは前回以降の増分だけを整理する

GIVEN 対象Subjectに整理済みKnowledgeがあり、前回のBrew後に未整理Knowledgeが追加されている
WHEN BrewがそのSubjectを再度処理する
THEN 新たに追加された未整理Knowledgeだけが整理対象になる
AND 既に整理済みのKnowledgeは再処理を理由に変更されない

## B-024: 同じ入力でのBrew再実行は変更を生まない

GIVEN 対象Subjectについて前回のBrew後に未整理Knowledgeが追加されていない
WHEN BrewがそのSubjectを再度処理する
THEN 新たなKnowledgeの作成、更新、関係変更は発生しない

## B-025: Brewは未整理Knowledgeを破棄できる

GIVEN Brewが保持しないと判断する未整理Knowledgeが入力に含まれている
WHEN Brewが対象Subjectを処理する
THEN その未整理Knowledgeは整理済みKnowledgeにならない
AND 承認対象にも検索結果にも現れない

## B-026: Brew結果から変更されたSubjectを識別できる

GIVEN Brewが一つ以上のSubjectのKnowledgeを変更する
WHEN 利用者がBrewの実行結果を確認する
THEN Knowledgeに変更が生じた各Subjectを識別できる

## B-027: 中断後は未完了分だけが再処理される

GIVEN Draw後段またはBrewが中断され、完了済みと未完了の処理対象が混在している
WHEN 同じ対象について処理が再実行される
THEN Draw後段が完了済みのFeedstockから未整理Knowledgeが重ねて作成されない
AND 整理済みのKnowledgeは再処理されない
AND 未完了のFeedstockと未整理Knowledgeだけが処理される

## B-028: Feedstockの検索と表示を維持する

GIVEN Drawによって保存されたFeedstockが存在する
WHEN 利用者が`knowbrew feedstock`で検索し、`knowbrew show <feedstock-id>`で表示する
THEN 本変更前と同じ検索条件で同じFeedstockを取得できる
AND 同じFeedstock IDの詳細を表示できる

## B-029: 承認によるpendingとactiveの遷移を維持する

GIVEN Brewで整理され、invalidatedでもsupersededでもないKnowledgeが存在する
WHEN 利用者がKnowledgeファイルの`approved`を変更する
THEN `approved`が`true`のKnowledgeはactiveになる
AND `approved`が`false`のKnowledgeはpendingになる

## B-030: Knowledgeのsuperseded遷移を維持する

GIVEN 現行のKnowledgeを正しくsupersedeする後継Knowledgeが存在する
WHEN Knowledgeライフサイクルが反映される
THEN 先行Knowledgeはsupersededになる
AND 後継Knowledgeとの関係が維持される

## B-031: Knowledgeのinvalidated遷移を維持する

GIVEN 現行のKnowledgeに無効化が記録される
WHEN Knowledgeライフサイクルが反映される
THEN そのKnowledgeはinvalidatedになる

## B-032: Distillは承認済みかつ現行のKnowledgeだけを使う

GIVEN 同じSubjectにactive、pending、superseded、invalidatedのKnowledgeが存在する
WHEN DistillがそのSubjectの文書を生成する
THEN activeかつ現行のKnowledgeだけが文書生成の入力になる
AND pending、superseded、invalidatedのKnowledgeは入力に含まれない

## B-033: SessionStartの文書組み立てを維持する

GIVEN 常時注入対象の文書と、作業場所に一致するSubjectおよび一致しないSubjectの注入対象文書が存在する
WHEN SessionStart用のコンテキストが組み立てられる
THEN 常時注入対象の文書が含まれる
AND 作業場所に一致するSubjectの文書が含まれる
AND 一致しないSubjectの文書は含まれない

## 要件IDとBehavior IDの対応表

| Requirement ID | Behavior ID |
| --- | --- |
| R-001 | B-001 |
| R-002 | B-002 |
| R-003 | B-003 |
| R-004 | B-004, B-005, B-006 |
| R-005 | B-007 |
| R-006 | B-008 |
| R-007 | B-009, B-010 |
| R-008 | B-011 |
| R-009 | B-011, B-012, B-013, B-014, B-015 |
| R-010 | B-016 |
| R-011 | B-017 |
| R-012 | B-018 |
| R-013 | B-020 |
| R-014 | B-021, B-022 |
| R-015 | B-023, B-024 |
| R-016 | B-025 |
| R-017 | B-026 |
| R-018 | B-027 |
| R-019 | B-018, B-019 |
| R-020 | B-028 |
| R-021 | B-029, B-030, B-031, B-032, B-033 |
