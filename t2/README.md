# T2: Payload Boundary Test

Purpose: Check deterministic payload length, bytes and CRC32 around packet boundaries.

Procedure: Exchange all listed lengths once per iteration; stop on first mismatch.

PASS criteria: Exact frame kind, sequence, length, pattern and CRC32 within deadline.

FAIL: framing/CRC, exact byte/length, identity, unexpected source/sequence or transport error.
TIMEOUT: the whole iteration does not finish by `-timeout`. ABORTED: SIGINT/SIGTERM.
Stop at the first non-PASS outcome. Never automatically reset, flush, retry, or conceal a failure.

## Build and run

From the project root (Linux ARM64, Go 1.27.4, TinyGo 0.44.0):

```sh
go build -o /tmp/t2-pc ./t2/pc/main.go
go vet ./t2/pc/main.go
go test -v ./t2/pc/main.go ./t2/pc/main_test.go
tinygo build -target=pico -scheduler=tasks -o /tmp/t2-tasks.uf2 ./t2/pico/main.go
tinygo build -target=pico -scheduler=cores -o /tmp/t2-cores.uf2 ./t2/pico/main.go
make test TEST=t2 SCHED=tasks COUNT=2
make test TEST=t2 SCHED=cores COUNT=2
```

Do not flash/run hardware tests until the physical target and wiring are verified.
Use the existing root README flashing prerequisites. Direct host: `/tmp/t2-pc -count 2`.
Common flags: `-count 100` (0 unlimited), `-port` (auto if omitted), `-timeout 5s`,
`-interval 100ms` (0 supported), `-csv` (new file only). Unknown flags/positional arguments are rejected.

## Framing and bounds

Binary frame: magic `A5 5A`, kind byte, producer byte, uint32 LE sequence, uint16 LE length,
0..512 payload bytes, uint32 LE CRC32 IEEE of header and payload. Total size = payload + 14.
Reads/writes may be fragmented or coalesced. Host reads exact frame lengths, validates CRC before interpretation;
host steady-state framing errors are never silently resynchronized. Pattern byte at offset i = low8(i*31 + seq*17 + producer*13).
`P`=PING, `R`=PONG; `C`=arm configuration, `A`=armed, `D`=device stream, `V`=verification/completion barrier.
Wire sequence arithmetic is modulo 2^32; the host CSV iteration counter is uint64.
Each completed round drains its finite stream/barrier before reusing the port. Sequence checks are within
this bounded stream, not a guarantee against replay across an entire uint32 sequence cycle.

Pico input storage is 526 bytes; receive polls at most 64 bytes per scheduling turn with 1ms sleep.
The installed CDC driver has its own finite queues: overload loss is a test observation, not proof of a specific driver bug.
`cdc.USB.Write` has no cancellation API and can block if its driver queue stops draining.
Host deadlines bound the observation; the firmware does not create unlimited replacement workers.
If unresponsive, preserve logs and heartbeat state, then manually use BOOTSEL/reflash the identified Pico.
Do not modify the driver, add sleeps/locks to hide symptoms, or infer a root cause from timeout alone.

## GPIO and results

GP28: independent goroutine, 500ms ON / 500ms OFF. GP27: latched parser/protocol/write error.
GP14/GP15/GP26: unused, initialized OFF. HIGH means ON (`activeHigh` constant); verify polarity and resistors.
Heartbeat activity alone does not establish USB or other-core health. Check it visually.

Direct and Make runs use `t2/results/<run-id>/metadata.json`, `console.log`, `results.csv`.
CSV includes timestamps (RFC3339Nano), monotonic elapsed ns, outcome/error, last successful operation,
USB identity before/after, and test observations. Skip `#` comment lines (Go `csv.Reader.Comment='#'`).
Direct runs and `make run` record firmware scheduler as unknown; a Make test records only the successful flash invocation,
not runtime readback. Installed TinyGo version includes LLVM; source hashes and parameters are saved.
Counts include recorded ABORTED/SKIPPED iterations, no unstarted work; failure rate denominator is PASS+FAIL+TIMEOUT.
No hardware behavior has been verified during implementation; socket unit tests are transport/parser checks only.

## Case-specific behavior

Required payloads: 0, 1, 31, 63, 64, 65, 127, 128, 255, 256, 511, 512. Additional 50/114/242/498-byte payloads make complete frames exactly 64/128/256/512 bytes. USB transaction boundaries still cannot be inferred from application reads. Each iteration covers every length and records successful length/checksum observations.

Optional `-lengths` selects up to 32 payload sizes, e.g. `-lengths 512` to isolate a boundary from a fresh
firmware state. The default includes every required size and the additional complete-frame boundary sizes.
Maximum complete frame size is 526 bytes, larger than the installed CDC RX ring (512 bytes).
With the intentional 1ms firmware polling yield, fast host writes may overflow that ring. This is a
possible application/queue-overload observation, not proof of a packet-boundary or synchronization defect.
The test does not silently add pacing or retry to hide this symptom. On first failure later sizes are not
attempted; isolate the failing and subsequent sizes in separate fresh runs before drawing conclusions.
Make example: `make test TEST=t2 COUNT=2 ARGS="-lengths 511,512"`.

The Pico hunts the magic prefix to find requests; outside-frame bytes are not application payload.
Host checks concern complete logical frames. T7–T10 additionally latch outside-window RX framing faults
in recovery status. Buffers are fixed in size, but frame copies/encoding and GC may affect timing;
these programs are not an allocation-free USB throughput benchmark.
