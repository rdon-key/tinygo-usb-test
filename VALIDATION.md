# Validation record

Hardware flashing, USB power switching, and long hardware runs have NOT been performed.
Tools: Go 1.27.4; TinyGo 0.44.0 / LLVM 22.1.4; Linux ARM64.
Caches: GOCACHE=/tmp/t1-go-cache XDG_CACHE_HOME=/tmp/t1-cache.
TinyGo builds are strictly sequential.

## Phase A review: T2–T6

- Host build, go vet and socket/parser unit tests passed for every case.
- Firmware builds passed for every T2–T6 case with both tasks and cores (10 UF2 builds).
- Make dry-run and parameter rejection checked; T0/T1 source hashes unchanged.
- Reviewed bounded framing, per-producer ordering, duplex cancellation, and reopen identity.
- CRC-valid wrong payloads and duplicated/out-of-order sequences are rejected.
- USB transport overload, application framing interleaving, and driver synchronization defects
  remain distinct hypotheses; no hardware failure/root cause has been established.
- Proceeded to Phase B only after this review and the Phase A builds/checks completed.

## Phase B review: T7–T10

- Host build, go vet and framing/transition socket unit tests passed for each case.
- All four updated firmware sources built with tasks and cores (8 final builds, sequential).
- Reviewed C/A/G arm/commit, independent scheduled Attach, observed USB removal/new address,
  joined old-FD workers, bounded recovery scanning, strict post-window verification, and worker return.
- Corrected outside-window RX fault recording and explicit background-worker completion before final builds.
- Raw old traffic cannot end recovery: only the fresh recovery nonce may do so.
- T9/T10 compilation can emit LLVM reserved-register/clobber warnings; build exit status was 0.
  Warning cause and runtime effects remain unverified; no toolchain changes were made.
- T10 CPU samples are observations, not core affinity guarantees or proof of the precise SE0 race.
  Installed 0.44.0 has no later pull-up guard; exact post-guard race requires additional version/instrumentation.
- Proceeded to Phase C after this review. No hardware transition tests were run.

## Phase C and final review: T11 / full suite

- T11 host build, go vet and 11 unit tests passed, including missing/unverified configuration,
  unknown JSON fields, shared-hub/capability rejection, exact single-port action arguments,
  fresh-descriptor recovery and bounded safety ON after an OFF error. All power commands were mocked.
- T11 firmware built successfully for tasks and cores. No physical switching was performed.
- T10 final workload-proof status was reviewed, unit-tested, and rebuilt for both schedulers.
- Final `GOCACHE=/tmp/t1-go-cache make check` exit code: 0.
  Records: `t1/results/20261112T223651-97n62m5l/` (console.log and metadata.json).
- New Go test functions: 96 total. Existing T1's 5 scenarios passed; T0 has no dedicated unit tests.
  Launcher tests: 10 passed. All T0–T11 host builds and go vet checks passed.
- All 20 final T2–T11 tasks/cores UF2 artifacts were checked for UF2 magic and 512-byte block alignment.
  Artifacts: `/tmp/tN-{tasks,cores}.uf2`.
- Final artifact/source SHA-256 manifest: `/tmp/cdctest-validation-artifacts.json`.
- Makefile dry-runs cover every target, all new IDs are accepted, unknown arguments are rejected,
  and help works from outside the project root.
- Offline host smoke checks used a guaranteed nonexistent port (T2–T10) or no power configuration (T11).
  They verified English summaries, one recorded FAIL/SKIPPED outcome, CSV columns/comments and unlimited-count
  display without opening a USB port or invoking power control. They are not hardware test results.
- T0/T1 firmware and host SHA-256 values match the pre-change values exactly.

## Remaining limits

- No firmware was flashed, no physical VBUS operation occurred, and no positive hardware communication,
  LED behavior, scheduling behavior, transition recovery, or long-duration stability was verified.
- T2's complete 526-byte maximum frame exceeds the installed 512-byte CDC RX ring; application polling
  and overload can explain loss. Use `-lengths` to isolate observations; do not call every timeout a USB race.
- T5 raw writes may expose application/API multi-producer framing assumptions, not necessarily a driver bug.
- T7–T10 require host-observed disappearance and same-device new-address return; missed short disconnects
  do not pass. Expected transition-window loss is logged explicitly. Host deadlines cannot cancel a stuck
  TinyGo Write or IRQ; manual target-only BOOTSEL/SWD recovery can be necessary.
- The exact SE0 post-guard race is not claimed reproduced. See T10 README for version and SWD requirements.
- T11 requires an independently measured, exclusive external hub/port receipt; per-cycle voltage is not
  automatically instrumented. Built-in/shared/root hubs and unverified configurations remain SKIPPED.
- LLVM reserved-register/clobber warnings from T9/T10 builds remain unexplained and must accompany any
  hardware failure report. Toolchain and driver sources were not modified.

Offline smoke records for this validation: `/tmp/cdctest-offline-4dgmtqyl/`.
Final firmware/source manifest and unit-test counts were checked after the last source changes.
