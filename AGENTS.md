# TinyGo USB CDC Test Project

## Purpose

This repository is a hardware-in-the-loop test environment for investigating
TinyGo USB CDC behavior on RP2040, including enumeration, communication,
Detach/Attach, reconnection, and concurrency-related failures.

The priority is **reproducible observations**, not complex test infrastructure.
Keep test programs small enough to understand and to attach to bug reports.
Distinguish a reproduced symptom from a confirmed root cause.

## Environment

- Host: Raspberry Pi 3, Linux ARM64 (`aarch64`)
- Host toolchain: Go 1.27.4
- Device toolchain: TinyGo 0.44.0, LLVM 22.1.4
- Device: RP2040-based Raspberry Pi Pico
- TinyGo target: `pico`
- Project directory: `/home/user/cdctest`
- Host is connected to Pico by USB and can observe USB enumeration.
- `udiskie` is configured to automount the Pico BOOTSEL (`RPI-RP2`) volume.
- USB host power switching may be available via `uhubctl`, subject to
  verification of the actual hub and port.

Do not assume the environment is unchanged: check actual devices, mountpoints,
and tool versions when relevant.

## Repository Layout

Organize tests by test case. Each case owns its Pico and host implementations.

```text
cdctest/
├── AGENTS.md
├── t1/
│   ├── pico/main.go    # TinyGo firmware
│   └── pc/main.go      # Go program running on Raspberry Pi
├── t2/
│   ├── pico/main.go
│   └── pc/main.go
└── ...
```

- Use `tN/pico` for RP2040 firmware and `tN/pc` for host-side control.
- Keep cases independent; avoid introducing a shared framework prematurely.
- Test-specific protocols, LED behaviors, timing, and recovery strategies
  belong to the individual test case.
- Provide a short explanation of each test's purpose, setup, PASS criteria,
  and failure conditions in that test's documentation or source comments.
- Do not reuse `t1` for a different test after results have been collected.

## RP2040 GPIO and LEDs

Five external status LEDs are connected to RP2040 GPIOs **GP14, GP15, GP26,
GP27, and GP28** (BCM numbers on the Raspberry Pi are NOT relevant here).

- **GP28 is always reserved for the firmware heartbeat LED.** Every Pico test
  must provide a heartbeat. Each test decides how to implement it and at what
  cadence; do not impose a shared implementation.
- GP14, GP15, GP26, and GP27 are available for test-specific indications.
- State what each test-specific LED means in that test's documentation.
- Confirm LED polarity and current-limiting resistors against the actual
  wiring; do not assume HIGH means ON without verification.
- A blinking heartbeat only demonstrates that its implementation is running.
  It does **not** prove that USB CDC, scheduling, or the other core is healthy.
- Never reassign GP28 to a USB status or PASS/FAIL indicator.

## USB and Power Control

The host may test USB physical disconnection/reconnection by switching USB
port power **when the attached hardware supports it**. `uhubctl` can be used
for this purpose.

- USB power OFF/ON is an allowed test operation after identifying the correct
  hub and port and confirming the expected scope of power switching.
- Never assume a fixed `uhubctl` location or port number; discover and verify
  it on the current hardware.
- Raspberry Pi 3's USB topology may have shared power or other devices on the
  same hub. Do not switch an entire hub or affect host networking accidentally.
- Do not shut down or power off the Raspberry Pi host.
- Distinguish these operations in code and logs:
  - physical VBUS power removal/restoration;
  - USB unplug/replug or host-side bus reset;
  - device-side software `Detach()` / `Attach()`;
  - entering/exiting RP2040 BOOTSEL mode.
- Do not assume `uhubctl` reporting OFF proves that VBUS was physically removed.
  Verify the observable effect when the distinction matters.
- Allow settling time and bounded deadlines for disconnection, enumeration,
  port creation, and CDC communication; do not treat them as one event.
- Do not assume a stable `/dev/ttyACM*` path or that `tinygo ports` uniquely
  identifies a board. Discover the actual device for each test.
- Do not assume `lsusb` detection means that CDC is ready for data transfer.

## Test Lifecycle and Results

The Raspberry Pi host program owns test orchestration and results.

- Make the iteration count configurable. Support both a finite count and
  unlimited execution until interrupted, when suitable for the test.
- Record the test case identifier, run start time, run end time, and total
  elapsed time.
- For every iteration, record its sequential number, start and end times,
  elapsed time, outcome, and relevant error or observation.
- Outcomes must distinguish **PASS**, **FAIL**, **TIMEOUT**, and **ABORTED**
  (or clearly document an equivalent set).
- Use the host clock for timestamps (prefer RFC 3339 with time zone) and
  monotonic elapsed-time measurements where available.
- Print progress during a run, including completed count and running totals.
- At the end, print the requested/completed iteration count, PASS/FAIL/
  TIMEOUT/ABORTED counts, start/end, duration, and failure rate with its
  denominator defined.
- Save machine-readable results, preferably CSV, with enough information to
  investigate individual failures. Put output in a clearly identified
  test-specific location; do not overwrite previous runs unintentionally.
- On Ctrl+C/SIGTERM, stop safely, preserve the results collected so far, and
  print an interrupted-run summary. Never count incomplete work as PASS.
- A test case must define objective PASS/FAIL/TIMEOUT criteria **before**
  implementing the test loop.
- An expected failure reproduction is a successful *experiment* only when
  its specified observation is verified; do not label arbitrary errors PASS.
- Keep evidence such as USB IDs, port paths, device state transitions, timing,
  and relevant command errors when those matter to the assertion.
- Recovery after failure (continue, power-cycle, or preserve the failed
  state for debugging) is test-specific. Do not silently recover in a way
  that hides the original symptom.

## Console Output

Apply these rules to T0–T12 and future tests when implementing or updating
their console output. Keep each test's purpose, measurements, and verdict
criteria unchanged; do not introduce a shared display framework.

1. **Opening summary:** show the test ID/name, a one-line purpose, available
   host/environment information, test-specific conditions, and requested
   iteration count. Never guess unavailable environment information.
2. **Progress and trial results:** use `[iteration/requested] OUTCOME elapsed`.
   Show PASS on one line. For FAIL, TIMEOUT, or ERROR, add relevant measurements
   and error details on indented lines. Show unlimited counts as `∞` or an
   equivalent explicit label. Long runs may suppress individual PASS lines
   if periodic progress still shows completed counts and running totals.
3. **Final summary:** prominently show the overall result according to that
   test's criteria, all outcome counts, completed/requested iterations, total
   duration, and CSV path. Include start/end times and the failure rate with
   its denominator. Print this summary on interruption whenever possible;
   never treat incomplete or interrupted work as PASS.

Console output is for human monitoring; CSV is the detailed record for
analysis. Preserve CSV conditions, timestamps, measurements, outcomes, and
error details when changing presentation. Keep unmeasured values distinct
from measured mismatches. Do not change protocols or verdicts for display.

Example (conditions and outcome categories depend on the test):

```text
==================================================
T12 - USB CDC RX Data Integrity
==================================================
Purpose : Compare received length and SHA-256 with sent data
Host    : windows/amd64 / COM12
Size    : 1024 B
Pause   : 0 ms
Observe : 3s
Count   : 1
--------------------------------------------------
[1/1] FAIL    3.004s
       Sent     : 1024 B
       Received : 576 B
       SHA-256  : MISMATCH
==================================================
RESULT: FAIL
==================================================
PASS=0  FAIL=1  TIMEOUT=0  ERROR=0  ABORTED=0
Completed: 1/1
Duration : 3.014s
CSV      : t12/results/t12-v2-....csv
```

## Build, Flash, and Monitor

- Use the stable, installed TinyGo release, not a development checkout,
  unless a test explicitly requires another version.
- Use standard Go on the Raspberry Pi host.
- Favor the Go standard library where practical. Add dependencies only when
  justified by the test's hardware access or functionality.
- Prefer small, direct build commands. For example, from the project root:

```sh
tinygo build -target=pico -o /tmp/t1.uf2 ./t1/pico/main.go
go build -o /tmp/t1-pc ./t1/pc/main.go
```

- Flash only when the test requires it and the correct target is identified:

```sh
tinygo flash -target=pico ./t1/pico/main.go
```

- The BOOTSEL volume may be mounted at `/media/user/RPI-RP2`; confirm the
  actual mountpoint using `lsblk` or `findmnt`.
- Do not mount over a stale mountpoint or create stacked mounts. USB
  re-enumeration may change `/dev/sdX`; avoid relying on drive letters.
- `tinygo flash -monitor` may fail to find a CDC port even when flashing
  succeeds. When diagnosing, separate build, flash, enumeration, and monitor.
- A one-shot `println` can finish before a serial monitor connects. Use a
  repeated or handshake-based test when verifying CDC output.
- Pico builds on the Pi 3 are slow. Avoid needless rebuilds, and do not
  delete Go/TinyGo build caches without a reason.

## Coding and Investigation Rules

- Implement the smallest useful reproducible test first.
- Define the hypothesis, stimulus, observable result, and timeout explicitly.
- Keep firmware timing and synchronization changes intentional: adding
  logging, sleeps, or locks may hide or change a race condition.
- Do not introduce unbounded blocking, uncontrolled busy loops, or arbitrary
  sleep-based PASS criteria.
- Do not conflate successful build, successful flash, USB enumeration,
  CDC readiness, and successful communication.
- Keep exact failure evidence and distinguish observations from hypotheses.
- Do not modify TinyGo source in this repository. Propose upstream fixes
  separately and only after reproduction and analysis.
- Do not change system-wide USB permissions, udev rules, sudoers, services,
  mount policy, network settings, or Raspberry Pi power settings without
  explicit approval.
- Do not install extra packages or perform destructive actions unless the
  requested test needs them and their effects are understood.

## Agent Workflow and Communication

1. Read this file and the target test case before changing code.
2. Summarize the intended test and measurable success criteria.
3. Implement only the requested test case and required support code.
4. Run static checks/builds when feasible; run hardware tests only when the
   target device and any power-control scope have been verified.
5. Report commands used, observations, PASS/FAIL/TIMEOUT counts, and any
   remaining uncertainty. Never claim a test passed without evidence.

- Communicate with the user in **Japanese**.
- Keep code, command names, and protocol identifiers in their conventional
  technical notation.
- Keep reports concise, factual, and reproducible.
- Ask before significant architecture or system-level changes; do not broaden
  the test scope on your own.

