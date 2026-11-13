# T11: USB Power Cycle Test

Purpose: physically remove/restore USB VBUS through one explicitly verified exclusive smart-hub port.

Procedure: check receipt and live topology/capability, PING/PONG, close CDC, OFF, observe USB disappearance,
wait the off duration, ON, observe a new USB address, open a fresh CDC node, PING/PONG.

PASS: explicit physical/exclusive switching verification, actual USB disappearance and same-device return,
new address, and correct bounded recovery PING/PONG. FAIL/TIMEOUT: an attempted verified cycle violates these conditions.
SKIPPED: receipt absent/invalid, tool unavailable, shared/unsupported hub, identity mismatch or verification failure.
ABORTED: signal; always make a bounded safety ON attempt if OFF may have occurred.
Stop on first non-PASS. No software Detach, DTR toggles or logical USB resets substitute for physical power switching.

## Build and run

From the project root (Linux ARM64, Go 1.27.4, TinyGo 0.44.0):

```sh
go build -o /tmp/t11-pc ./t11/pc/main.go
go vet ./t11/pc/main.go
go test -v ./t11/pc/main.go ./t11/pc/main_test.go
tinygo build -target=pico -scheduler=tasks -o /tmp/t11-tasks.uf2 ./t11/pico/main.go
tinygo build -target=pico -scheduler=cores -o /tmp/t11-cores.uf2 ./t11/pico/main.go
make test TEST=t11 SCHED=tasks COUNT=2
make test TEST=t11 SCHED=cores COUNT=2
```

Do not flash/run hardware tests until the physical target and wiring are verified.
Use the existing root README flashing prerequisites. Direct host: `/tmp/t11-pc -count 2`.
Common flags: `-count 100` (0 unlimited), `-port` (auto if omitted), `-timeout 5s`,
`-interval 100ms` (0 supported), `-csv` (new file only). Unknown flags/positional arguments are rejected.

## Framing and bounds

Binary frame: magic `A5 5A`, kind byte, producer byte, uint32 LE sequence, uint16 LE length,
0..512 payload bytes, uint32 LE CRC32 IEEE of header and payload. Total size = payload + 14.
Reads/writes may be fragmented or coalesced. Host reads exact frame lengths, validates CRC before interpretation;
host steady-state framing errors are never silently resynchronized. Pattern byte at offset i = low8(i*31 + seq*17 + producer*13).
`P`=PING, `R`=PONG; `C`=arm configuration, `A`=armed, `D`=device stream, `V`=verification/completion barrier.
Wire sequence arithmetic is modulo 2^32; the host CSV iteration counter is uint114.
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

Direct and Make runs use `t11/results/<run-id>/metadata.json`, `console.log`, `results.csv`.
CSV includes timestamps (RFC3339Nano), monotonic elapsed ns, outcome/error, last successful operation,
USB identity before/after, and test observations. Skip `#` comment lines (Go `csv.Reader.Comment='#'`).
Direct runs and `make run` record firmware scheduler as unknown; a Make test records only the successful flash invocation,
not runtime readback. Installed TinyGo version includes LLVM; source hashes and parameters are saved.
Counts include recorded ABORTED/SKIPPED iterations, no unstarted work; failure rate denominator is PASS+FAIL+TIMEOUT.
No hardware behavior has been verified during implementation; socket unit tests are transport/parser checks only.

## Physical power verification is required

Default operation is **SKIPPED**. No hub/port is guessed. The example JSON is deliberately disabled.
Use `-power-config /absolute/path/to/verified.json` only after an operator has explicitly verified:

1. The exact external hub location, VID/PID and single port control the intended Pico directly.
2. The exact `uhubctl -e -l <hub> -p <port> -a off/on` operation physically removes/restores VBUS,
   measured at the target (meter or power analyzer), not just logical data disconnect.
3. Other devices, host networking and hub upstream power are unaffected; check the full topology and shared rails.
4. Verification with `-e` alone works; a USB3 hub requiring companion-hub control is unsupported by this test.

These manual verification operations require separate explicit hardware authorization. Do not infer voltage
removal from an OFF status, lsusb disappearance or the heartbeat. No installation, sudo, udev or system settings
are changed automatically. Missing authorized access gives SKIPPED.

The JSON schema is the example file. Set the three verified booleans only after measurement; record a
non-future RFC3339 `verified_at`, evidence (1..2048 bytes), exact current hub VID/PID (lowercase hex),
USB connection location and the Pico serial (empty only if the device has none). Unknown JSON fields are rejected.
The configured connection must be the direct child of that exact hub/port. Receipt size is at most 8192 bytes.
Re-verify after any wiring/topology/hardware change. Per-cycle VBUS voltage is not measured by this program;
its physical-switching conclusion depends on that explicit receipt plus observed USB state changes.

Each cycle rechecks live hub identity/class, exact port status and `ppps` capability. Root hubs and known
shared Raspberry Pi LAN951x/2514 hubs are refused even if the receipt claims otherwise. A misleading hub
advertisement alone cannot establish isolation; manual exclusive-scope verification remains mandatory.

Flags: `-off 1s` (250ms..10s), `-reconnect 15s` (max 1m), `-power-config` (no default).
`-timeout` bounds communication; the finite overall budget includes command/disconnect/reconnect phases.
Every uhubctl subprocess has a 3s deadline. OFF and ON are separate commands so disappearance is observed
before ON. After Ctrl+C or an OFF-side failure, a fresh 3s context attempts ON on the same verified hub/port.
It rechecks hub identity and never switches a different/unknown hub. Restoration failures are recorded;
manual recovery may still be required. This safety attempt does not erase the original error or count PASS.

```sh
# Only after explicit physical verification and hardware authorization:
make test TEST=t11 SCHED=cores COUNT=2 ARGS="-power-config /absolute/path/verified.json -off 1s -reconnect 15s"
```

The host implementation uses `uhubctl -e` (exact hub; disables USB3 companion-hub handling),
never `-f`, `-R`, whole-hub selection or a guessed port. See the
[upstream usage and hardware notes](https://github.com/mvp/uhubctl#usage) and
[USB3 duality note](https://github.com/mvp/uhubctl#usb-30-duality-note).
[Pi onboard power rails can be shared](https://github.com/mvp/uhubctl#raspberry-pi-turns-power-off-on-all-ports-not-just-the-one-i-specified);
a verified external per-port hub is required here.

Config receipt, SHA-256, exact hub tool results, disappearance/return times and USB identity changes are
stored in CSV details. GPIO remains GP28 heartbeat, GP27 latched protocol error, GP14/15/26 unused OFF.
No physical power switching or hardware recovery has been executed during implementation.

The Pico hunts the magic prefix to find requests; outside-frame bytes are not application payload.
Host checks concern complete logical frames. T7–T10 additionally latch outside-window RX framing faults
in recovery status. Buffers are fixed in size, but frame copies/encoding and GC may affect timing;
these programs are not an allocation-free USB throughput benchmark.

Re-verify the physical receipt after tool, kernel or hub firmware changes as well as topology changes.
Record the uhubctl version and exact `-e` command used for measurement in the evidence text.
