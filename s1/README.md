# S1 - USB CDC RX Data Loss Reproducer

[日本語版 README](README.ja.md)

## Overview

A minimal reproducer for investigating data loss during USB CDC reception.

This test checks whether USB CDC receive flow control works correctly.

The PC sends 1024 bytes to a Raspberry Pi Pico. After reading the first byte, the Pico sleeps for 5 seconds without reading any additional data. It then resumes reading for up to 3 seconds and checks whether all 1024 bytes were received correctly by comparing the received byte count and SHA-256 hash.

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

Replace the port name with the actual serial port.

Each execution runs a single test. Reset the Pico before running the test again.

## Result Criteria

| Result | Condition |
|---|---|
| PASS | All 1024 bytes are received and the SHA-256 hash matches |
| FAIL | The received byte count or SHA-256 hash does not match |
| TIMEOUT | Sending or receiving the result does not complete within 10 seconds |
| ERROR | A communication error occurs or the result format is invalid |

The PC sends 1024 bytes containing the sequence 0–255 repeated four times.

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
