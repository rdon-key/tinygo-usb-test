# t1: USB CDC正常系ベースライン

## 目的と判定

RP2040 PicoとLinuxホストのCDCで、連番付き要求・応答を逐次反復する。
仮説は「通常接続のまま各PINGに対応するPONGを期限内に受信できる」。
刺激は `PING <sequence>\n`、観測は受信した完全な行のバイト一致とホスト経過時間。
電源制御、バスリセット、Detach/Attach、BOOTSEL移行はテストに含めない。

- PASS: `PONG <送信と同じsequence>\n` と完全一致する応答を期限内に受信。
- FAIL: 応答不一致、受信上限64バイト超過、デバイス消失、送受信・検出・open・設定エラー。
- TIMEOUT: 送信開始から指定期限までに完全な正しい応答を確認できない。部分応答も記録。
- ABORTED: 実行中の通信をSIGINT/SIGTERMで中断。反復間の中断は架空の反復を追加しない。

最初のFAIL/TIMEOUT/ABORTEDで停止し、バッファ消去・再open・再送・復旧は行わない。
遅延応答を次の反復と混同しないためでもある。送信済みデータの取り消しはできない。
有限回の終了と `-count 0` の無制限実行に対応する。

## ファームウェアとLED

TinyGo 0.44.0のインストール済みソース
`/usr/local/lib/tinygo/src/machine/usb/cdc/{cdc,usbcdc}.go` を参照。
Picoランタイムで初期化される `cdc.USB` の `ReadByte` / `Write` / `DTR` を使用する。
CDCに要求以外の出力はしない。入力は固定32バイト、返信も固定32バイト。
形式は `PING ` + 1～20桁のASCII数字 + LF。数字列を数値変換せず保持するため先頭の0も保存される。
CRLF、空の連番、数字以外、長すぎる行は不正。長すぎる入力は次のLFまで捨てる。
不正要求への返信はなく、GP27で示す。要求は1件ずつ送り応答を待つこと。

| GPIO | 意味 |
|---|---|
| GP28 | ハートビート: 500msごとに反転（周期1秒）、CDC待ちと独立したgoroutine |
| GP14 | 正常要求かつDTR有効を確認すると30ms点灯 |
| GP15 | 応答を送信goroutineに渡すと30ms点灯。ホスト受信完了の証明ではない |
| GP26 | 未使用、消灯 |
| GP27 | 不正要求/DTR無効で200ms点灯、送信エラー・送信待ち1秒超過で常時点灯 |

点灯中も待機せず、期限を比較する。メインループは最大32バイト処理後に1ms sleep。
`activeHigh` 定数で極性変更可能。通電前に実配線の極性と電流制限抵抗を確認すること。
ハートビートが点滅してもCDCや他の処理の健全性は保証しない。

CDCの `Write` にはキャンセルAPIがなく、内部バッファ満杯時に待ち続け得る。
そのため送信goroutineは1個のみで、同時に1件だけ送信する。
1秒超過を検出した場合、メインは要求処理を永久停止し、GP27を点灯させる。
送信goroutine自体を強制停止することはできない。後から送信が戻っても要求処理は再開しない。
この状態ではファームウェア再起動が必要。USBの内部動作停止はホストの期限でも検出する。
`Write` 成功はUSB転送完了の証明ではなく、最終判定はホストのPONG受信で行う。

## ビルド

プロジェクトルートで実行する。依存パッケージなし。ホストコードはLinux向け。

```sh
tinygo version
go version
tinygo build -target=pico -o /tmp/t1.uf2 ./t1/pico/main.go
go build -o /tmp/t1-pc ./t1/pc/main.go
go vet ./t1/pc/main.go
go test -v ./t1/pc/main.go ./t1/pc/main_test.go
```

キャッシュが書き込み禁止の実行環境では、既存キャッシュを削除せず次を使う。
初回は再コンパイルに時間がかかる。

```sh
GOCACHE=/tmp/t1-go-cache XDG_CACHE_HOME=/tmp/t1-cache tinygo build -target=pico -o /tmp/t1.uf2 ./t1/pico/main.go
GOCACHE=/tmp/t1-go-cache go build -o /tmp/t1-pc ./t1/pc/main.go
GOCACHE=/tmp/t1-go-cache go vet ./t1/pc/main.go
GOCACHE=/tmp/t1-go-cache go test -v ./t1/pc/main.go ./t1/pc/main_test.go
```

## 接続確認と書き込み

他のシリアルモニターを閉じ、対象PicoをUSBトポロジー・VID/PID・製品名・シリアル番号（あれば）で確認する。

```sh
lsusb
ls -l /dev/serial/by-id /dev/ttyACM*
tinygo ports
lsblk -o NAME,LABEL,MOUNTPOINTS
findmnt
```

対象を一意に確認できた場合だけ、通常の書き込みを実行する。

```sh
tinygo flash -target=pico ./t1/pico/main.go
```

対象が複数ある場合は他のボードを外すなどして一意にする。
手動BOOTSEL書き込みの場合は対象の `RPI-RP2` の実際のマウント先を確認し、
ビルド済み `/tmp/t1.uf2` をそのボリュームへコピーする。
ドライブ文字・マウント先を固定しない。再マウントやシステム設定変更は不要。
書き込み成功、USB列挙、CDCポート作成、通信成功を別々に確認する。

## 実行

```sh
/tmp/t1-pc
/tmp/t1-pc -count 1000 -timeout 5s -interval 100ms
/tmp/t1-pc -count 0
/tmp/t1-pc -port /dev/serial/by-id/実際の対象 -csv t1/results/my-run.csv
```

| オプション | 初期値 | 意味 |
|---|---|---|
| `-count` | 100 | 反復回数、0は無制限 |
| `-port` | 自動 | 明示時もsysfsのUSB親デバイス情報を検証 |
| `-timeout` | 5s | 1要求の送信と応答全体の期限（正の値） |
| `-interval` | 100ms | 反復間の待機（0以上） |
| `-csv` | 自動 | 新規ファイル限定のCSV出力先 |

自動検出は `/sys/class/tty/ttyACM*` のUSB親を辿り、インストール済みpicoターゲットに
対応するVID:PID `2e8a:000a` の候補がちょうど1台の場合だけ選択する。
これはt1ファームウェア固有の識別子ではない。複数候補・異なるVID/PIDでは
実機情報を確認して `-port` を指定する。製品名だけやTinyGoのボード名だけで選択しない。
非USB ttyは拒否する。DTR/RTSを有効化し、115200・8N1・raw・フロー制御なしでopenする。
1200bpsによるBOOTSEL移行は使用しない。既存受信データは捨てず、不一致として記録する。

通信はnonblocking read/writeと最大1msの中断可能な待機を使用する。
USB消失はsysfsのtty存在確認（100ms間隔）とread/writeエラーで検出する。
ポート名を再検出して自動復旧しない。タイムアウトはOSスケジューリングで多少超過し得る。
検出・open・ioctlは通信期限の対象外で、通常のLinuxローカルデバイス操作を前提とする。

## ログと集計

自動CSVは実行ディレクトリ基準の `t1/results/t1-<日時・ナノ秒・タイムゾーン>.csv`。
プロジェクトルートで起動すること。明示パスの親ディレクトリは事前に作成する。
`O_EXCL` で既存ファイルを上書きしない。各行でCSV flushとファイルsyncを行う。
この保存処理とコンソール表示の時間は通信の計測時間に含まれず、反復周期には影響する。

CSVにはtest ID、run開始時刻、予定回数、連番、開始/終了RFC3339Nano時刻、
単調時計による経過ns、判定、エラー、ポート、VID/PID、USBシリアル、製品名、sysfs親パスを記録する。
コンソールはすべて英語で表示する。開始時にTest ID and Name、Purpose、Test Procedure、
Verification Method、Start Time、Iteration Count、Communication Timeout、Test Interval、
USB CDC Portを案内する。自動検出時は `Automatic detection`、選択後に実際のポートを表示する。
正常な反復は1行で連番/予定回数・判定・経過時間だけを表示し、FAIL/TIMEOUT/ABORTED時は
既存のエラー内容・実際の受信内容（記録されている場合）も表示する。詳細なUSB識別情報はCSVに残す。

```text
[0001/0100] PASS    12.3ms
[0002/0100] PASS    11.8ms
[0003/0100] TIMEOUT 5.002s  waiting for matching PONG; sent=7 response=""
```

上記は表示例。最初の異常で停止する動作は変更しない。
`-count 0` は開始/終了時に `Unlimited (count=0)`、進捗に `[0001/unlimited]` と表示する。
コンソールの経過時間は読みやすく丸めるが、CSVのナノ秒精度・RFC3339Nano日時は維持する。

終了時はResult（`COMPLETED` / `FAILED` / `INTERRUPTED`）、Start Time、End Time、
Total Duration、Requested / Completed Iterations、PASS / FAIL / TIMEOUT / ABORTED、
Failure Rate、CSV Fileを表示する。通信・ログ保存の異常はFAILED、シグナル中断はINTERRUPTED。
Failure Rateは `(FAIL+TIMEOUT)/(PASS+FAIL+TIMEOUT)`。ABORTEDは分母から除外し、分母0も表示する。
Completed Iterationsは従来どおり結果を記録した反復数で、
`PASS+FAIL+TIMEOUT+ABORTED` と一致する。ABORTEDを含むことを表示し、未着手分を追加しない。
反復間の中断では新しいABORTED行を作らず、ResultをINTERRUPTEDとする。

CSVの先頭に `# ` で始まる英語コメントを付ける。内容はTest ID and Name、Purpose、
Host and Device、Test Procedure、Protocol（PING/PONG）、Verification Method、GP28 Heartbeat、
Test Parameters（count、timeout、interval、要求ポート、CSV出力先）、Test Start Time。
自動検出の場合、コメントのportは `Automatic detection` とし、実際のポートとUSB識別情報は
従来どおり各反復のデータ列に記録する。コメントの後に既存の15列のCSVヘッダーと詳細記録が続く。
**CSV読み込み時は先頭のコメント行をスキップする必要がある。**
Goの `encoding/csv` では `reader.Comment = '#'`、Pythonのpandasでは
`pd.read_csv(path, comment='#')` を指定する。
終了コードは0=正常終了、1=通信/初期化異常、2=引数/ログ異常、130=中断。
ログ書き込み失敗時はエラーを表示して停止する。保存できなかった行の永続化は保証できない。

`main_test.go` はUnix socketで通信を模擬し、正しい返信・連番不一致・無応答・
部分応答・中断の判定を確認する。USB、termios、実機のLEDを検証するものではない。
実機では有限回を先に実行し、CSVとLEDを確認してから長時間テストする。

## 今回の検証記録（2026-11-12、実機通信未実施）

- TinyGo 0.44.0 / Go 1.27.4 / LLVM 22.1.4を確認。
- 上記の `/tmp` 専用キャッシュ指定でPicoビルド成功（`/tmp/t1.uf2`）。
- ホスト `go build`、`go vet` 成功。模擬通信の5シナリオすべて成功。
- 存在しないポート指定で `PASS=0 FAIL=1 TIMEOUT=0 ABORTED=0`、
  CSVへのエラー保存と同じCSVパスの上書き拒否を確認。
- sysfsでは `2e8a:000a`、製品名 `Pico` を確認。一方、実行環境に
  `/dev/ttyACM0` がなく、`lsusb` は `libusb: -99` で失敗。
- 書き込み、実機PING/PONG、LED極性・抵抗・点滅、長時間安定性、
  実USB消失時の挙動は未確認。USB列挙情報だけを通信成功の証拠にしていない。

ログ形式統一後もGoビルド、`go vet`、既存の単体テスト5シナリオが成功。
`/tmp` の検証用コピーでUSB部分をソケットに置き換え、通常終了・応答不一致・
タイムアウト・中断の表示、集計とCSV行数の一致、9行のコメントと従来の15列を確認した。
これはホストのログ確認であり、実機USB通信の検証ではない。

## Makefileからの管理

共通のビルド・書き込み・実行・環境記録は [ルートREADME](../README.md) を参照。
例: `make build TEST=t1 SCHED=cores`、`make test TEST=t1 SCHED=tasks COUNT=1000 TIMEOUT=5s INTERVAL=100ms`。
`make run TEST=t1` は実機のschedulerを検証せずunknownと記録する。
Makefile経由ではCSVに `# Run Environment:` コメントが追加され、同じ実行ディレクトリの
`metadata.json`、`console.log` と対応する。既存のCSV列、引数、判定と中断処理は変更しない。
