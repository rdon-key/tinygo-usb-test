# TinyGo USB CDCテスト管理

T0はLEDの目視確認、T1はPING/PONG正常系ベースライン。T2以降は独立したストレス・遷移試験。
個別仕様は [T0](t0/README.md)、[T1](t1/README.md) を参照。
Makefileは既存のプロトコル・Pico処理・ホストの判定条件を変更しない。
Python 3標準ライブラリ、GNU Make、インストール済みGo/TinyGoを使用する。追加パッケージ不要。

## コマンド

以下のflash/run/testは実機を操作する。対象・配線を確認してから明示的に実行する。

```sh
make help
make check
make build TEST=t0 SCHED=tasks
make build TEST=t1 SCHED=cores
make flash TEST=t0 SCHED=tasks
make run TEST=t0 COUNT=2 HOLD=1s
make test TEST=t1 SCHED=cores COUNT=1000 TIMEOUT=5s INTERVAL=100ms
make test TEST=t1 SCHED=tasks COUNT=0 PORT=/dev/serial/by-id/実際の対象
```

別ディレクトリからは `make -f /home/user/cdctest/Makefile help` のように指定する。
補助スクリプトが常にプロジェクトルートへ移動してからコマンドを実行する。
Makeの同一呼び出しは直列実行する。実機を使うターゲットを別端末で並行実行しないこと。
キャッシュへの書き込みが制限される環境では、既存キャッシュを削除せず例えば
`GOCACHE=/tmp/t1-go-cache XDG_CACHE_HOME=/tmp/t1-cache make check` とする。

| 変数 | 初期値 | 用途 |
|---|---|---|
| `TEST` | `t1` | `t0` ～ `t11` |
| `SCHED` | `tasks` | Picoのビルド・書き込みに使う `tasks` / `cores` |
| `COUNT` | T0: `1`、その他: `100` | 既存の `-count`、`0` は無制限 |
| `PORT` | 空（自動） | 既存の `-port`。flash前の対象確認にも使用 |
| `HOLD` | `1s` | T0の `-hold` |
| `TIMEOUT` | `5s` | T1の `-timeout` |
| `INTERVAL` | `100ms` | T1の `-interval` |
| `ENUM_TIMEOUT` | `20` | flash成功後の再列挙待ち期限、秒数（最大3600） |
| `ARGS` | 空 | テスト固有引数（例 `ARGS="-frames 32"`）。未対応・重複引数は拒否 |

`make build` はホストとUF2をビルドし、書き込まない。UF2は
`build/<test>-<scheduler>.uf2`（同じ組み合わせは再ビルド時に更新）。
`make check` は全テストのGoビルド・`go vet`・単体テストと補助スクリプトの単体テストを行う。
T0には現在専用のGo単体テストがなく、`[no test files]` と表示される。
Picoビルドや実機テストはcheckに含めない。

## flashと再列挙

`make flash` と `make test` は以下を確認する。

1. sysfsでVID:PID `2e8a:000a` のCDCデバイスと実際のキャラクターデバイスを確認。
2. Pico/BOOTSEL（`2e8a:000a` / `2e8a:0003`）が接続全体で1台であることを確認。
   `PORT` 明示時はそのデバイスとの一致も確認。
3. 確認したポートを指定し、
   `tinygo flash -target=pico -scheduler=<SCHED> -port=<確認したポート> <test>/pico/main.go`
   を実行。失敗・中断すればホストテストを開始しない。
4. `make test` は同じUSB接続位置、同じシリアル番号（存在する場合）、新しいUSBアドレスで
   CDCポートが生成され、300ms安定して見えるまで待つ。
5. 期限内に確認できた実際のポートをホストに渡す。再列挙失敗でもホストは開始しない。

最小構成のため、**flash開始時にCDCポートが必要**。初回ファームウェア導入や
BOOTSEL状態だけのボードへの書き込みは、各テストの手動BOOTSEL手順を使う。
複数ボードからの自動選択、電源制御、自動復旧は行わない。
USBポートは移動せず、実行中に別ボードへ差し替えないこと。
シリアル番号がない場合、接続位置とVID/PIDだけでは実機個体の入れ替えを識別できない。
VID/PIDはテスト固有ではない。対象は実配線・トポロジーも含めて確認する。

再列挙確認はCDCが通信可能なことの証明ではない。実際の通信は既存ホストプログラムが担当する。
flash時のBOOTSEL移行は書き込みのためであり、Detach/Attach・電源試験ではない。
再列挙の観測はflash成功後から始める。以前と同じUSBアドレスしか見えなければ
成功とせずタイムアウトする。flashコマンド自体には補助スクリプト独自の時間制限を加えない。

## 結果と設定の対応付け

build/flash/run/test/checkの実行ごとに、既存ログを上書きしない一意な
`<test>/results/<日時>-<ランダム値>/` を作成する。

- `metadata.json`: 実行ID、開始/終了、単調時計の経過時間、終了コード、Go/TinyGoバージョン、
  ホスト環境、テスト名、要求target/scheduler、実行条件、ソースSHA-256、
  flashコマンド・対象USB情報（該当時）、ホスト起動コマンドとCSV保存先。
- `console.log`: 実行コマンドと子プロセスの標準出力・標準エラー。
- `results.csv`: ホストを起動した場合の既存形式の結果。既存列と精度は維持。
- `<test>-pc`: その実行でビルドしたホスト実行ファイル（checkでは両テスト）。

`make run` は書き込み履歴を流用しない。
`SCHED=cores` を指定しても実機のスケジューラは **unknown** と記録する。
同様に実機ファームウェアのtarget・ビルド時TinyGoバージョンもunknown。
記録されるインストール済みツールのバージョンと実機を書き込んだツールのバージョンは区別する。
`make flash` 後の別の `make run` でも、この制約は同じ。

`make test` は同じ実行で成功したflashコマンドのtarget・scheduler・TinyGoバージョンを記録する。
これは指定した書き込みコマンドの証拠であり、実機内設定のreadback検証ではない。
要求した `SCHED` は常に `scheduler_requested`、flashによる設定記録は `firmware` に保存する。
`cores` がビルドできることと実機上の正常動作は別。LED・通信の結果を確認する。

CSV冒頭には `# Run Environment: <JSON>` としてホスト開始直前のメタデータを保存する。
同じ実行IDとCSVパスで `metadata.json` に対応する。終了時にJSON側へ終了情報を追記するため、
CSVのスナップショットには実行終了時刻・最終終了コードは含まれない。
コメントは既存同様、Goでは `csv.Reader.Comment = '#'` 等で読み飛ばす。

この受け渡しのため、ホストは任意の環境変数 `CDCTEST_METADATA`（メタデータファイル）を読み、
各行をCSVコメントへ追加する。T0のみ `CDCTEST_CSV` でCSV保存先を指定可能。
T1は従来の `-csv` を使用する。環境変数未指定の直接実行は従来どおり。
既存のCLI引数・通信期限・判定・Ctrl+C時のT0消灯処理は変更しない。
補助スクリプトはSIGINT/SIGTERMを子プロセスへ転送し、ホストの終了処理完了を待って結果を保存する。

## 実装時の検証

- Makefile全ターゲットのdry-run、ルート外からのhelp起動、Python構文を確認。
- `make check`: T0/T1のGoビルド・`go vet`、既存T1テスト5シナリオ、補助スクリプト7件を確認。
- `make build TEST=t0 SCHED=tasks`、`make build TEST=t1 SCHED=cores` は成功。
- Pico側ソースは変更前後のSHA-256一致を確認。
- flash/run/testによる実機操作は未実施。実機での書き込み、再列挙待ち、
  スケジューラごとのCDC・LED動作と終了時の消灯は未確認。

## Test matrix and recommended order

順序はT0→T1→T2→T6→T3→T4→T5→T7→T8→T9→T10→T11を推奨。
各ケースを少回数・tasksで確認してからcoresで比較する。T5はraw/serializedの両方を個別に実行する。
実機への書き込み・電源操作・長時間テストは、別途明示的な実行指示と対象確認が必要。

| ID | Name | 前提・刺激 | PASSの観測条件 |
|---|---|---|---|
| [T0](t0/README.md) | USB CDC and LED Operation Check | LED配線確認 | 目視で点灯順序、ホストはSENTのみ |
| [T1](t1/README.md) | USB CDC Baseline Test | 通常接続 | 期限内の正確なPING/PONG |
| [T2](t2/README.md) | Payload Boundary Test | 0～512 byteの決定的payload | 全長の長さ・内容・CRC32一致 |
| [T3](t3/README.md) | Burst Transfer Test | 有限window内の連続要求 | 全連番・順序・barrier一致 |
| [T4](t4/README.md) | Full-Duplex Test | 独立した双方向sender | 各方向のデータ・連番・受信数を期限内に確認 |
| [T5](t5/README.md) | Concurrent TX Test | Pico producer 2～4個 | producer別frame・連番・CRC32一致 |
| [T6](t6/README.md) | Port Reopen Test | ホスト側close/open | 同一USB識別・アドレス、再open後の通信 |
| [T7](t7/README.md) | Idle Detach/Attach Test | idle中の予約software遷移 | 消失・新アドレス・回復通信 |
| [T8](t8/README.md) | Detach During RX Test | ホスト送信中の予約遷移 | RX刺激確認と遷移窓外の整合性・回復 |
| [T9](t9/README.md) | Detach During TX Test | Pico送信中の予約遷移 | TX刺激・worker復帰・回復後の双方向通信 |
| [T10](t10/README.md) | Multicore Detach Race Test | 双方向通信＋scheduler workload | 遷移・worker復帰・回復。厳密SE0 raceは別途SWDが必要 |
| [T11](t11/README.md) | USB Power Cycle Test | 専用hub/portと物理VBUS切替の明示的確認が必要 | 消失・再列挙・回復通信。未確認構成ではSKIPPED |

T2以降のframeはバイナリで、既存T1のASCII PING/PONGとは別プロトコル。
テストごとに正しいfirmwareを書き込む。T0/T1のコード・プロトコル・判定は変更しない。
各新ケースは自身のGoソースだけでビルド可能で、共通のテストフレームワークには依存しない。

```sh
make test TEST=t2 SCHED=tasks COUNT=2
make test TEST=t3 SCHED=cores COUNT=2 INTERVAL=0 ARGS="-burst 32 -window 4 -request-interval 0"
make test TEST=t4 SCHED=cores COUNT=2 ARGS="-frames 32"
make test TEST=t5 SCHED=cores COUNT=2 ARGS="-frames 32 -producers 4 -mode raw"
make test TEST=t5 SCHED=cores COUNT=2 ARGS="-frames 32 -producers 4 -mode serialized"
make test TEST=t10 SCHED=cores COUNT=2 ARGS="-offsets 20ms,50ms,100ms -off 750ms -reconnect 15s"
```

T2以降では未対応Make変数（例 `HOLD`）や未対応の `ARGS` を拒否し、黙って無視しない。
パラメータを実際に使えるかは各ケースの `-h` とREADMEを参照。
直接起動時も `<test>/results/<run-id>/` にmetadata/console/CSVを作る。
直接起動と `make run` のfirmware schedulerはunknown。CSVのSKIPPED/ABORTEDを失敗率の分母から除外する。
`Completed Iterations` は記録済みoutcome数で、PASS/FAIL/TIMEOUT/ABORTED/SKIPPEDの合計。

## Investigation limits

GP28 heartbeatだけでUSBや他coreの健全性は判定しない。
TinyGoのUSB WriteにはキャンセルAPIがない。host deadlineで異常を観測して停止し、
Picoの無応答状態はログとLED/SWDの状態を保存してから手動BOOTSELで回復する。
T9/T10ビルドではLLVMのreserved-register/clobber警告を観測した。コンパイル成功と
実機上の安全・正常動作は別であり、警告の原因・影響は未確認。
インストール済みRP2040 E5 workaroundはGP15を一時的に再設定するため、
列挙時のGP15 LEDだけを厳密なTX指標にしない。
T10の単なるtimeoutやDetach/Attach実行を、PR #6085 / Issue #6253の正確なSE0 race再現と呼ばない。
詳細な制約とSWD手順はT10 READMEを参照。

## T11 physical power prerequisites

T11はhub/portを自動選択しない。`t11/power-config.example.json` は意図的に無効な雛形。
専用外部hubのexact portで物理VBUSを測定し、他デバイス・ネットワークへ影響しないことを
確認したreceiptがなければSKIPPED。root hubや既知のPi共有電源hubは拒否する。
`uhubctl -e` に限定し、別hubへの自動連動操作、force、全hub操作、論理Detach代用は行わない。
必要な確認・設定手順と制約はT11 READMEを参照。今回は物理切替は一度も実施していない。

新ケースの終了コードは0=完了、1=FAIL/TIMEOUT、2=引数/ログ異常、77=SKIPPED、130=中断。
Makeは子の非0終了をエラーとして報告するが、SKIPPEDとFAILの区別はCSV/console/metadataの
ホスト終了コードを参照する。T0/T1の既存終了動作は維持する。

## T2–T11 implementation validation

結果・フェーズごとのレビュー・未確認事項は [VALIDATION.md](VALIDATION.md) を参照。
新規Goテスト96件、既存T1の5シナリオ、管理スクリプト10件が成功し、
T2–T11のtasks/cores全20構成を直列でビルドした。T0/T1のソースはハッシュ一致で未変更。
実機の書き込み・電源操作・通信/LED確認・長時間試験は行っていない。
