# S1 - USB CDC RX Data Loss Reproducer

[日本語版 README](README.ja.md)

## Overview

A minimal reproducer for investigating data loss during USB CDC reception.

This test checks whether USB CDC receive flow control works correctly.

The PC sends the number of bytes specified by `-size` (default: 1024) to a Raspberry Pi Pico. After reading the first byte, the Pico sleeps for 5 seconds without reading any additional data. It then resumes reading until the 3-second deadline, without knowing the send size or stopping early at 1024 bytes. It incrementally computes SHA-256 over all received data. The PC compares the received byte count and SHA-256 hash with the sent data.

## How to Run

Run all commands from the repository root.

### 1. Flash the Pico

Use TinyGo 0.43.0.

```bash
tinygo flash -target=pico -scheduler=tasks ./s1/tinygo/main.go
```

### 2. Run the PC Program

Windows:

```bash
go -C s1/host run . -port COM12
```

Linux:

```bash
go -C s1/host run . -port /dev/ttyACM0
```

Replace the port name with the actual serial port. `-size` specifies a positive byte count; the default is 1024. Zero and negative values are rejected.

Examples:

```bash
go -C s1/host run . -port COM12 -size 512
go -C s1/host run . -port COM12 -size 513
go -C s1/host run . -port COM12 -size 1024
go -C s1/host run . -port COM12 -size 2048
```

Each execution runs a single test. Reset the Pico before running the test again.

## Result Criteria

| Result | Condition |
|---|---|
| PASS | The received byte count equals the specified size and the SHA-256 hash matches |
| FAIL | The received byte count or SHA-256 hash does not match |
| TIMEOUT | Sending or receiving the result does not complete within 10 seconds |
| ERROR | A communication error occurs or the result format is invalid |

The PC generates the specified number of bytes with `data[i] = byte(i)` (the sequence 0–255 repeated as needed) and computes SHA-256 over the entire payload.

The Pico returns the received byte count and SHA-256 hash as ASCII text. The byte count is a zero-padded 10-digit decimal number, followed by a 64-character hexadecimal SHA-256 hash.

## Observed Result

Tested on November 13, 2026.

- Board: Raspberry Pi Pico (RP2040)
- TinyGo: 0.43.0
- Scheduler: `tasks`
- PC: Windows AMD64
- USB CDC port: COM12

Output:

```text
S1 - USB CDC RX Data Loss Reproducer
Sent     : 1024 B
Received : 513 B
SHA-256  : MISMATCH
Result   : FAIL
```

The PC sent 1024 bytes, but the Pico reported receiving only 513 bytes. The SHA-256 hash did not match either.

This result indicates a possible loss of received data. The exact point of data loss and its root cause have not yet been determined.

## Related Information

See [T12 - USB CDC RX Data Integrity](../t12/README.md) for more detailed investigation and testing.
