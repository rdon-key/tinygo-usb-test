# S1 - USB CDC RX Data Loss Reproducer

## 概要

USB CDCで、受信データの欠損を調べる再現コード。

受信フロー制御が正しく動作するか確認する。

PCから1024バイトのデータを送信する。Pico側では最初の1バイトを受信した後、5秒間スリープする。その後、残りのデータを最大3秒間読み出し、受信バイト数とSHA-256が正しいことを確認する。

## 実行方法

以下のコマンドは、すべてリポジトリのルートディレクトリから実行する。

### 1. Picoへの書き込み

TinyGo 0.43.0を使用する。

```bash
tinygo flash -target=pico -scheduler=tasks ./s1/tinygo/main.go
```

### 2. PC側の実行

Windows：

```bash
go -C s1/host run . -port COM12
```

Linux：

```bash
go -C s1/host run . -port /dev/ttyACM0
```

ポート名は環境に合わせて変更する。

1回の実行で1回試験する。再実行する場合はPicoをリセットする。

## 判定

| 結果 | 条件 |
|---|---|
| PASS | 1024バイト受信し、SHA-256も一致 |
| FAIL | 受信バイト数またはSHA-256が不一致 |
| TIMEOUT | 10秒以内に送信・結果受信が完了しない |
| ERROR | 通信エラーや結果形式の異常 |

PCは0～255を4回繰り返した1024バイトを送信する。

Picoは実際に読み出したデータの受信バイト数とSHA-256をASCII形式で返す。受信バイト数はゼロ埋め10桁、SHA-256は64桁の16進数とする。

## 確認結果

2026年11月13日、以下の環境で確認。

- ボード：Raspberry Pi Pico（RP2040）
- TinyGo：0.43.0
- Scheduler：tasks
- PC：Windows AMD64
- USB CDC：COM12

実行結果：

```text
S1 - USB CDC RX Data Loss Reproducer
Sent     : 1024 B
Received : 513 B
SHA-256  : MISMATCH
Result   : FAIL
```

1024バイトの送信に対して、Pico側で確認できた受信数は513バイトであり、SHA-256も一致しなかった。

この結果は受信データの欠損が疑われることを示している。欠損の発生箇所や原因は未確定。

## 関連情報

詳細な調査と検証は [T12 - USB CDC RX Data Integrity](../t12/README.md) を参照。
