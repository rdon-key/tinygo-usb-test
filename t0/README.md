# T0: USB CDC・LED動作確認

PiからPicoへ命令を送り、4個のLEDが1個ずつ順に点灯することを目視確認する。
CDCの通信性能やデータ整合性を厳密に測定するテストではない。T1とは独立している。
USB電源操作、Detach/Attach、バスリセットは行わない。

## 接続とLED

RP2040側のGPIO番号を使う。各LEDの極性と電流制限抵抗を実配線で確認してから通電する。
HIGH点灯を想定し、必要ならPicoソースの `activeHigh` 定数を変更する。

| GPIO | 役割 |
|---|---|
| GP14 | 制御対象LED 1 |
| GP15 | 制御対象LED 2 |
| GP26 | 制御対象LED 3 |
| GP27 | 制御対象LED 4 |
| GP28 | ハートビート専用、500ms点灯・500ms消灯（周期1秒） |

起動時、制御対象LEDはすべて消灯。GP28は別goroutineでUSB接続状態に依存せず点滅する。
GP28の点滅だけではCDCやシステム全体の健全性を保証できない。

## プロトコルと動作

ASCII・LF終端。下記の9コマンドだけを受理する。返信や他のCDC出力はない。

```text
LED 14 ON
LED 14 OFF
LED 15 ON
LED 15 OFF
LED 26 ON
LED 26 OFF
LED 27 ON
LED 27 OFF
ALL OFF
```

不正命令（空行、CRLF、GP28への操作を含む）は無視する。
固定32バイトの入力バッファを使用し、長すぎる行はLFまで捨てる。
受信は1ターン最大32バイト処理後に1ms sleepし、ハートビートの実行機会を確保する。
点灯時間や順番はPicoで管理せず、Piが送る命令だけで制御する。

Piは最初に `ALL OFF` を送り、その後GP14、GP15、GP26、GP27それぞれに
ON→`-hold`待機→OFFを送る。これを1反復として `-count` 回実行する。
次のLEDのONの前に前のLEDのOFFを必ず送る。GP28はこの点灯制御の対象外。
正常終了・Ctrl+C/SIGTERM・通信異常時には、可能な限り `ALL OFF` を再送する。
開始時と終了処理では先頭にLFを送り、以前の部分コマンドを終端してから `ALL OFF` を送る。

## ビルド

プロジェクトルート `/home/user/cdctest` で実行する。
TinyGo 0.44.0・ターゲット `pico`、ホストはGo 1.27.4のLinux向け。
追加パッケージやGoモジュールは不要。

```sh
tinygo version
go version
tinygo build -target=pico -o /tmp/t0.uf2 ./t0/pico/main.go
go build -o /tmp/t0-pc ./t0/pc/main.go
go vet ./t0/pc/main.go
```

ホームのキャッシュが書き込み禁止の環境では `/tmp` を使用する。
以下は今回、T1のビルドで作成済みのキャッシュを再利用したコマンド。
T1のソースや出力ファイルを変更するものではない。既存キャッシュを削除しない。

```sh
GOCACHE=/tmp/t1-go-cache XDG_CACHE_HOME=/tmp/t1-cache tinygo build -target=pico -o /tmp/t0.uf2 ./t0/pico/main.go
GOCACHE=/tmp/t1-go-cache go build -o /tmp/t0-pc ./t0/pc/main.go
GOCACHE=/tmp/t1-go-cache go vet ./t0/pc/main.go
```

## 対象確認と書き込み

他のシリアルモニターを閉じ、USBトポロジー、VID/PID、製品名、シリアル番号（存在すれば）で
書き込み先Picoを一意に確認する。TinyGoのボード名だけで判断しない。

```sh
lsusb
ls -l /dev/serial/by-id /dev/ttyACM*
tinygo ports
lsblk -o NAME,LABEL,MOUNTPOINTS
findmnt
```

対象を確認してから書き込む。

```sh
tinygo flash -target=pico ./t0/pico/main.go
```

複数台ある場合は他の対象を外すなどして一意にする。
手動BOOTSELの場合は、確認した `RPI-RP2` ボリュームの実際のマウント先へ
`/tmp/t0.uf2` をコピーする。ドライブ文字やマウント先を固定しない。
書き込み、USB列挙、CDCポート生成、実際のLED動作はそれぞれ別に確認する。

## 実行

```sh
/tmp/t0-pc
/tmp/t0-pc -count 10 -hold 1s
/tmp/t0-pc -count 0 -hold 500ms
/tmp/t0-pc -port /dev/serial/by-id/実際の対象 -count 1
```

| オプション | 初期値 | 意味 |
|---|---|---|
| `-count` | 1 | 4個を順に操作する反復回数。0は無制限 |
| `-port` | 自動 | 実際のUSB CDCポート |
| `-hold` | 1s | 各LEDの点灯待機時間。正のdurationを指定 |

自動検出は `/sys/class/tty/ttyACM*` のUSB親を調べ、VID:PID `2e8a:000a`
（インストール済みpicoターゲットの識別子）の候補が1台の場合だけ選択する。
ポート名は固定しない。このVID/PIDはT0固有ではないため、別ファームウェアとの区別はできない。
複数候補や異なるVID/PIDでは実機情報を確認して `-port` を指定する。
明示指定でもUSB親情報を読み、ポート、VID/PID、製品名、シリアル、sysfsパスをログに残す。
非USB ttyは受け付けない。

ホストは115200・8N1・raw・フロー制御なしで設定し、DTR/RTSを有効化する。
1200bpsによるBOOTSEL移行は使わない。シリアル設定を行うためLinux専用。
送信はnonblockingで、**各コマンド2秒**の期限を設ける。
Ctrl+C/SIGTERMで点灯待機と通常送信を中断し、独立した2秒の期限で最後の消灯送信を試みる。
無期限の送信待ちや `tcdrain` は使用しない。期限はOSスケジューリングで多少超過し得る。
検出・open・ioctlは通常のローカルLinux USB CDCデバイス操作を前提とする。

## ログ・判定と目視確認

コンソール表示はすべて英語。開始時にTest ID and Name、Purpose、Test Procedure、
Verification Method、Start Time、Iteration Count、LED Hold Duration、USB CDC Portを表示する。
自動検出時は最初に `Automatic detection`、検出後に選択した実際のポートを表示する。
通常の進捗は各GPIOのON/OFF送信状況と反復開始からの経過時間、および反復終了時の
所要時間・集計に絞る。長いタイムスタンプ、USB内部情報、コマンド全文は繰り返さない。

```text
[0001] GP14 ON  sent  (+0s)
[0001] GP14 OFF sent  (+1.001s)
[0001] GP15 ON  sent  (+1.002s)
...
[0001] SENT  elapsed=4.005s  completed=1 SENT=1 FAIL=0 TIMEOUT=0 ABORTED=0
```

表示はホストがON/OFF命令を送信したことを示し、実際のLED状態ではない。
エラー時は対象イベント、反復番号、コマンド、原因を表示する。
終了時に開始/終了時刻、総実行時間、予定・着手・完了反復数、判定別件数、エラー数、
送信失敗率、中断有無、目視確認が必要なこと、CSV保存先を表示する。
`Errors` は初期化・送信失敗/期限超過・CSV保存・closeのエラー件数。
同じ送信異常を反復集計でもう一度数えず、中断自体はエラー数に含めない。
CSVは実行ディレクトリ基準の `t0/results/t0-<日時・ナノ秒・タイムゾーン>.csv` に新規保存し、
既存ファイルは上書きしない。プロジェクトルートで起動すること。
CSVには実行開始/予定回数、コマンド、反復、終了処理のイベントとUSB識別情報を記録し、
イベントごとにflush・syncする。ログ保存失敗時は停止し、消灯送信を試みる。
従来のCSV列構成と詳細なイベント記録は維持する。

CSVの先頭には `# ` で始まる英語のコメント行を付ける。内容はTest ID and Name、Purpose、
Host and Device、Test Procedure、LED GPIO Assignment、Heartbeat Behavior、Verification Method、
Test Parameters（count、hold、要求ポートと検出後のポート）、Test Start Time。
その後に従来のCSVヘッダーとイベント行が続く。
**読み込み時は先頭のコメント行をスキップする必要がある。** 例えばGoの `encoding/csv` では
`reader.Comment = '#'`、Pythonのpandasでは `pd.read_csv(path, comment='#')` を指定する。
コマンド列にはLFが含まれるため、イベント行は通常のCSVパーサーで読むこと。

反復を始める前に定義するホスト側の判定は次のとおり。

- **SENT**: 4個すべてのON・待機・OFF処理が完了した。PASSの代わりに使う送信完了状態。
- **FAIL**: 送信エラー等。最初の異常で反復を停止する。
- **TIMEOUT**: コマンドの送信処理が2秒の期限に達した。
- **ABORTED**: 実行中の反復をシグナルで中断した。

`completed` はSENTの反復数、`attempted` は中断・失敗も含む着手数。
未着手・未完了をSENTにしない。送信失敗率の分母は `SENT+FAIL+TIMEOUT`、分子は
`FAIL+TIMEOUT`。これは**反復の送信処理**についての集計で、LED動作の失敗率ではない。
初期化・初回消灯・終了消灯の異常は別イベントとして記録し、反復数に含めない。
終了コードは0=処理完了、1=初期化/送信等の異常、2=引数/ログ異常、130=反復等の中断。

**SENTはカーネルがデータを受け付けたことだけを示し、USB転送完了・Picoでの解釈・LED点灯成功を保証しない。**
Picoから返信がないため、ホストはLEDの動作を判定できない。全体ログにも
`LED result=VISUAL_UNCONFIRMED` と表示する。実際の合否は次を目視確認して判断する。

1. USB CDC未接続でもGP28が500msごとに点灯・消灯する。
2. GP14→GP15→GP26→GP27の順に、1個ずつ指定時間だけ点灯する。
3. 指定回数繰り返し、正常終了後に4個とも消灯する。
4. 点灯中にCtrl+Cを押し、可能なら4個とも消灯する。GP28は引き続き点滅する。

USB切断・電源消失・ファームウェア停止等では終了時の消灯を保証できない。
ホストが送信エラーを検出せずにPicoが命令を受け取れない場合もあるため、
同時点灯の有無や消灯成功も目視で確認する。異常時に自動再接続・再起動は行わない。

## 今回の検証

2026-11-12: TinyGo 0.44.0 / Go 1.27.4 / LLVM 22.1.4を確認。
上記のキャッシュ指定でPicoとホストのビルド、ホストの `go vet` は成功。
T1の4ファイルは作業前後のSHA-256一致を確認。
sysfsでは `2e8a:000a` が見えるが、この実行環境に `/dev/ttyACM*` はなく、
`lsusb` は `libusb: -99` で失敗。BOOTSELボリュームも確認できていない。
**Picoへの書き込み、実機への命令送信、配線・極性・抵抗、LEDの点灯順序・時間、
ハートビート、中断時の実際の消灯は未確認。**

ホスト単体では、無効な `-hold` の拒否、存在しないポートでの初期化エラーと
反復0回のCSV保存を確認した。さらに `/tmp` の検証用コードでUnix socketを用い、
通常送信・キャンセル・送信バッファ満杯時の期限終了を確認した。
これらはUSB、GPIO、シリアル設定、実機での終了消灯の検証ではない。

## Makefileからの管理

共通のビルド・書き込み・実行・環境記録は [ルートREADME](../README.md) を参照。
例: `make build TEST=t0 SCHED=tasks`、`make test TEST=t0 SCHED=cores COUNT=2 HOLD=1s`。
`make run TEST=t0` は実機のschedulerを検証せずunknownと記録する。
Makefile経由ではCSVに `# Run Environment:` コメントが追加され、同じ実行ディレクトリの
`metadata.json`、`console.log` と対応する。直接実行時の引数と既定動作は変更しない。
