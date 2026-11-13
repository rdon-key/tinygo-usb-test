## 試験目的

USB CDCでHostが送信したデータとTinyGoが受信したデータを比較し、データの欠落や内容の不一致を確認する。受信バッファ容量や内部実装は判定の前提としない。

## 試験内容

1. HostはTinyGoに送信データ量、待機時間、観測時間を通知する。
2. TinyGoは準備完了をHostに通知する。
3. Hostは指定したデータ量を送信する。
4. TinyGoは待機時間中、受信データを読み出さない。
5. 待機終了後、TinyGoは観測時間中にデータを読み出す。TinyGoは受信量とSHA-256をHostに通知する。
6. Hostは送信データ量とSHA-256を受信結果と比較し、合否を判定する。

TinyGoは準備完了の通知直前から待機時間を計る。観測時間は読み出し再開後から計る。

## 試験条件

| 引数 | 条件 | 既定値・範囲 |
| --- | --- | --- |
| `-size` | 送信データ量 | `4096`、1 B～16 MiB |
| `-pause` | 待機時間 | `1s`、0～60秒、整数ms |
| `-observe` | 観測時間 | `3s`、1ms～60秒、整数ms |
| `-count` | 試行回数 | `1`、`0`は中断まで繰り返す |
| `-timeout` | 準備完了・結果通知を待つ猶予 | `5s`、100ms～60秒 |

Hostの送信期限は、準備完了の通知を受けてから待機時間＋観測時間とする。結果受信の期限は、さらに`-timeout`を加えた時点とする。

## 実行手順

WindowsのGit BashとLinuxで同じ手順を使う。リポジトリ直下から開始する。

### 1. TinyGo側のビルドと書き込み

```bash
tinygo version
cd t12/tinygo
mkdir -p ../../build
tinygo build -target=pico -scheduler=tasks -o ../../build/t12-v2-pico.uf2 .
```

このビルドはTinyGo 0.43.0で確認済み。`-target`は使用するボードを指定する。現在の対応ターゲットは`pico`のみ。

対象Picoを接続し、書き込む。

```bash
tinygo flash -target=pico -scheduler=tasks .
```

Hostとファームウェアは両方とも今回のv2形式に更新する。旧形式とは通信できない。

### 2. ポートの確認

```bash
tinygo ports
```

対象ボードのポートを確認し、他のシリアルモニターを閉じる。`-port`は必須。ポートの自動検出は行わない。

### 3. Hostの実行

```bash
cd ../pc
```

最初に64 B・待機なしで確認する。以下のポート名は実際の値に置き換える。

```bash
# Windows
go run . -port COM11 -size 64 -pause 0ms
# Linux（Raspberry Pi ARM64）
go run . -port /dev/ttyACM0 -size 64 -pause 0ms
```

次に4096 B・待機1秒で試験する。

```bash
# Windows
go run . -port COM11 -size 4096 -pause 1s
# Linux
go run . -port /dev/ttyACM0 -size 4096 -pause 1s
```

`-size`には512、513など任意の範囲内の送信量を指定できる。HostはPASS以外で停止する。Ctrl+Cで中断できる。異常後に再実行する場合は、記録とLEDを確認してからボードを再接続する。

### 対応状況

| 構成 | 確認状況 |
| --- | --- |
| Raspberry Pi Pico（RP2040）、TinyGo 0.43.0、`pico` / `tasks` | v2のビルド成功。実機再試験は未実施 |
| Windows AMD64 Host | ビルド・単体テスト成功。v2の実機通信は未確認 |
| Linux ARM64 Host（Raspberry Pi） | クロスビルド成功。実行・実機通信は未確認 |
| ESP32系 | 未対応 |

`t12/tinygo/main.go`は試験処理、`board_pico.go`はUSB接続先とGPIO・LED制御を担当する。使用したTinyGo 0.43.0では、ESP32-S3のUSB Serial/JTAGは`machine.USBCDC`を使用し、Picoの`machine/usb/cdc.USB`とは異なる。USB OTGも別の通信経路であり、現時点ではどちらもT12の対応対象に含めない。

### PicoのLED表示

| GPIO | 表示 |
| --- | --- |
| GP14 | 試験中 |
| GP15 | 読み出し待機中 |
| GP26 | 読み出し中 |
| GP27 | 受信量不一致またはファームウェア異常（点灯を保持） |
| GP28 | ハートビート（500msごとに反転） |

実配線のLEDの極性と電流制限抵抗を確認する。現行コードはHIGHで点灯する配線を前提とする。ハートビートは、その処理の動作を示す。

## 判定基準

| `outcome` | 意味 |
| --- | --- |
| `PASS` | Hostが全量を送信し、受信量とSHA-256が一致した。試行終了の確認応答も送信できた |
| `FAIL` | 有効な結果を取得したが、受信量またはSHA-256が一致しなかった |
| `TIMEOUT` | 規定時間内に処理が完了しなかった |
| `ABORTED` | 実行中の試行が中断された |
| `ERROR` | 送信未完了、通信エラー、無効な制御メッセージなどで処理を完了できなかった |

`integrity`はデータ比較だけを記録する。一致は`PASS`、不一致は`FAIL`、送信未完了や有効な結果を取得できなかった場合は`UNKNOWN`となる。通信エラーやTIMEOUTだけではデータ欠落と判断しない。

比較対象は観測時間内に受信したデータ。この比較だけでは恒久的な欠落と遅延到着を区別できない。

## 結果

Hostは実行ごとに、リポジトリ内の`t12/results/t12-v2-日時-*.csv`を新規作成する。作業ディレクトリを変えても既定の保存先は変わらない。`-out`で保存先を変更できる。相対パスはリポジトリ直下を基準とする。

| 確認する項目 | CSV列 |
| --- | --- |
| 試行結果・データの一致・エラー | `outcome`、`integrity`、`detail` |
| 送信指定量・Hostが書き込めた量・TinyGoの受信量 | `size`、`host_written`、`received` |
| 送受信のハッシュ値 | `sent_sha256`、`received_sha256` |

CSVには試験条件、試行ごとの開始・終了時刻と所要時間、送信開始までの遅延、最初の読み出し時刻、実待機時間も記録する。v2の`pause_ms`と`observe_ms`は整数のミリ秒値とする。

HostはコンソールとCSV末尾の`summary`に各結果の件数と総時間を記録する。失敗率は`(FAIL+TIMEOUT+ERROR)/(PASS+FAIL+TIMEOUT+ERROR)`とし、中断は含めない。中断時も、それまでの結果と集計を保存する。

### 旧形式のWindows実機結果

既存の`t12-日時-*.csv`は旧形式のまま保持する。次は2026-11-13のTinyGo 0.43.0、RP2040、Windows COM11での実測例。観測時間は3秒。

| 送信量 | 待機時間 | 受信量 | データ比較 | CSV |
| --- | --- | --- | --- | --- |
| 64 B | 0秒 | 64 B | PASS | [16:23:05](results/t12-20261113-162305-161155938.csv) |
| 4096 B | 約1秒 | 512 B | FAIL | [16:23:48](results/t12-20261113-162348-3429181637.csv) |
| 513 B | 約1秒 | 512 B | FAIL | [16:25:35](results/t12-20261113-162535-48748211.csv) |
| 512 B | 約1秒 | 512 B | PASS | [16:26:41](results/t12-20261113-162641-3794669895.csv) |

これらは当該構成での観測値であり、他のターゲットに共通する境界値を示すものではない。旧形式で記録された他の試行も同じディレクトリに保持している。v2の実機結果はまだない。
