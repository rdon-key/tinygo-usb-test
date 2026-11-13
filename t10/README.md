# T10: Multicore Detach Race Test

Purpose: Multicore Detach Race Test using supported TinyGo USB device APIs, not physical VBUS switching.

Procedure: initial PING/PONG; C/A arm; G commit; scheduled Detach/Attach without further commands;
observe disappearance and same-device return with a new address; fresh recovery exchange and strict post-window PING/PONG.

PASS: disappearance observed, identity matched, new address, worker return/status valid, no outside-window
RX errors, required traffic stimulus observed, and recovery PING/PONG verified.
FAIL: unexpected data/identity/status, driver/transport error outside the transition window.
TIMEOUT: bounded phase or overall iteration deadline. ABORTED: signal. SKIPPED: required RX/TX stimulus was not observed.
Stop on first non-PASS and preserve failed state; no automatic reset or unbounded recovery.

## Build and run

From the project root (Linux ARM64, Go 1.27.4, TinyGo 0.44.0):

```sh
go build -o /tmp/t10-pc ./t10/pc/main.go
go vet ./t10/pc/main.go
go test -v ./t10/pc/main.go ./t10/pc/main_test.go
tinygo build -target=pico -scheduler=tasks -o /tmp/t10-tasks.uf2 ./t10/pico/main.go
tinygo build -target=pico -scheduler=cores -o /tmp/t10-cores.uf2 ./t10/pico/main.go
make test TEST=t10 SCHED=tasks COUNT=2
make test TEST=t10 SCHED=cores COUNT=2
```

Do not flash/run hardware tests until the physical target and wiring are verified.
Use the existing root README flashing prerequisites. Direct host: `/tmp/t10-pc -count 2`.
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

GP28: independent goroutine, 500ms ON / 500ms OFF. Other LEDs are described below, initialized OFF. HIGH means ON (`activeHigh` constant); verify polarity and resistors.
Heartbeat activity alone does not establish USB or other-core health. Check it visually.

Direct and Make runs use `t10/results/<run-id>/metadata.json`, `console.log`, `results.csv`.
CSV includes timestamps (RFC3339Nano), monotonic elapsed ns, outcome/error, last successful operation,
USB identity before/after, and test observations. Skip `#` comment lines (Go `csv.Reader.Comment='#'`).
Direct runs and `make run` record firmware scheduler as unknown; a Make test records only the successful flash invocation,
not runtime readback. Installed TinyGo version includes LLVM; source hashes and parameters are saved.
Counts include recorded ABORTED/SKIPPED iterations, no unstarted work; failure rate denominator is PASS+FAIL+TIMEOUT.
No hardware behavior has been verified during implementation; socket unit tests are transport/parser checks only.

## Transition controls and expected loss

Flags: `-offsets 20ms,50ms,100ms` (rotate per iteration, 0..2s each, at most 32),
`-off 750ms` (100ms..10s, software detach only), `-reconnect 15s` (up to 1m).
Offsets/off are encoded in milliseconds (sub-millisecond fractions are truncated).
`-timeout` bounds pre-arm communication and recovery; overall iteration also has a finite budget for offsets,
detached duration and reconnect. This does not make internal CDC Write cancellable.
Make example: `make test TEST=t10 SCHED=cores COUNT=2 ARGS="-offsets 20ms,50ms -off 750ms -reconnect 15s"`.

C carries uint16 delay_ms/off_ms; A confirms arm. G commits the independent timer. After G,
no additional host command is needed for Attach. Expected loss/corruption window: successful G send through
fresh recovery R and S status. CRC/sequence errors outside this window fail. Recovery scanning is allowed
only in this window, with a maximum 526-byte buffer, 32768 discarded bytes and a finite deadline.
It is not general silent error recovery. Final PING/PONG uses strict framing again.

The host polls sysfs for actual USB device removal, then a changed address at the same connection,
matching serial if provided. It joins traffic workers before closing old descriptors and opens a fresh node.
If disappearance was too short to observe, the experiment does not pass; increase `-off`.
The host cannot force exact bus-level overlap; insufficient valid traffic stimulus yields SKIPPED after recovery.

S status contains tx_returned, detach/attach/RX core snapshots, NumCPU, background core,
latched RX errors outside the window and valid transitional RX frame count. Core IDs are sampled directly
from public RP2040 `rp.SIO.CPUID.Get()` immediately before the relevant operation, not inferred from goroutines.
They do not establish IRQ core placement or the exact instant of interleaving.

API source inspected: installed TinyGo 0.44.0 `src/machine/machine_rp2040_usb.go`:
`machine.USBDev.Detach()` clears the DP pull-up; `Attach()` sets it. Neither means physical VBUS removal.
The program never writes TinyGo internal state or alters the driver. Parser state is reset at known transitions;
this is application framing recovery during expected loss, not driver synchronization.

GPIO: GP28 heartbeat; GP14 transition armed/active; GP15 TX worker active (T9/T10); GP26 scheduler work (T10);
GP27 latched outside-window parser/error or TX-worker-not-returned indication. Confirm HIGH polarity/resistors.
If USB becomes unresponsive, record CSV, host error and LED state; then manually BOOTSEL/reflash the identified board.
No host timeout proves whether the cause was IRQ, scheduler, parser or USB TX.

No flashing or hardware transition tests have been performed during implementation.

## Scope relative to PR #6085 and Issue #6253

References: [PR #6085](https://github.com/tinygo-org/tinygo/pull/6085),
[Issue #6253](https://github.com/tinygo-org/tinygo/issues/6253).
These describe the RP2040 enumeration workaround waiting for SE0 and broader unsynchronized USB state transitions.
This test schedules concurrent application activity and Detach/Attach; a timeout alone does not establish
that the exact core-0 SE0 wait/core-1 Detach interleaving occurred.

The installed 0.44.0 source inspected here has an unconditional SE0 loop in
`src/machine/machine_rp2040_usb_fix_usb_device_enumeration.go`, called from the BUS_RESET IRQ path.
It does not have the later pull-up check discussed in the PR. Therefore reproducing the precise
"Detach after that check" interleaving requires an unmodified installed version containing that check,
as well as manual instrumentation; this suite does not add the check or modify TinyGo source.

Manual instrumentation procedure (not automatically executed):

1. Connect a verified SWD debugger to the target Pico. Save the UF2, compiler versions, source hashes,
   host log and exact installed TinyGo source. Do not debug/reset other USB devices.
2. Inspect the installed source, set breakpoints in the BUS_RESET IRQ handler and enumeration workaround,
   and identify the actual CPU executing each breakpoint. Do not assume goroutine placement.
3. With the target enumerated, arm a timed T10 cycle and generate a target-only host reset/SE0 stimulus
   using an operator-controlled USB host/analyzer. Halt core 0 at the relevant check/wait location;
   allow the detach goroutine to run on the other core using the debugger's per-core controls.
   A debugger/probe without independent core control cannot force this interleaving.
4. Capture both cores' PCs/stacks, SIE_CTRL pull-up bit, SIE_STATUS line state, IRQ status, and elapsed wait.
   For a version with the guard, verify the guard was passed before the other core cleared the pull-up.
5. Observe whether the wait persists and whether the scheduled Attach releases it. Record the actual
   core IDs and register sequence. SWD halting changes timing; compare with a normal run.
6. Preserve the failed state before manual target-only BOOTSEL recovery. If the guard/version,
   controllable SE0 stimulus, per-core SWD access or register evidence is absent, report this exact
   race reproduction as **unverified / requiring additional support**, not PASS.

On `tasks`, run the same application as a control. Even on `cores`, all samples may be core 0;
NumCPU=2 does not prove the critical operations ran on different cores. GP26 indicates bounded
scheduler workload; USB IRQ placement is not instrumented by the firmware.

Recovery requires the fresh recovery nonce; delayed old PING frames cannot end the transition window.
The firmware waits at most 2s after that nonce for both its TX and optional background worker to return.
A new round is never started with a previous background worker still alive. Status byte 6 records background return.
Valid X traffic outside the transition window and malformed framing outside it latch an RX error.

The installed TinyGo RP2040 E5 enumeration workaround temporarily reconfigures GP15 internally.
This suite does not change that driver behavior. GP15 LED activity during enumeration must not be
interpreted as precise TX/core-state evidence. Use USB tracing/SWD for that distinction.

TX-stream builds can emit LLVM reserved-register/clobber warnings (see T9). A successful build alone
is not runtime validation. No compiler/runtime source is modified to suppress these warnings.

T10 additionally records status byte 7 after at least one bounded background-work chunk actually ran.
If no chunk was observed, the host reports SKIPPED rather than claiming the workload was exercised.
This also distinguishes the T10 workload from the simpler T9 firmware; it still does not prove core placement.

The Pico hunts the magic prefix to find requests; outside-frame bytes are not application payload.
Host checks concern complete logical frames. T7–T10 additionally latch outside-window RX framing faults
in recovery status. Buffers are fixed in size, but frame copies/encoding and GC may affect timing;
these programs are not an allocation-free USB throughput benchmark.
