#!/usr/bin/env python3
"""Interactive TinyGo USB CDC batch, with manual recovery after failures."""
import subprocess
import time
from datetime import datetime
from pathlib import Path

ROOT = Path.home() / "cdctest"
USB_PORT = "1-1.4"  # Pico's verified USB topology on this Raspberry Pi
REPORT = ROOT / f"batch-{datetime.now():%Y%m%d-%H%M%S}.log"

jobs = []
for test in ("t5", "t7", "t8", "t9", "t10"):
    for sched in ("tasks", "cores"):
        modes = ("raw", "serialized") if test == "t5" else (None,)
        for mode in modes:
            args = (
                f"-frames 32 -producers 4 -mode {mode}" if mode
                else "-offsets 20ms,50ms,100ms -off 750ms -reconnect 15s" if test == "t10"
                else ""
            )
            jobs.append((test, sched, args))


def record(s):
    print(s, flush=True)
    with REPORT.open("a", encoding="utf-8") as f:
        f.write(s + "\n")


def attr(path, name):
    try:
        return (path / name).read_text().strip()
    except OSError:
        return ""


def pico_usb():
    found = []
    for p in Path("/sys/bus/usb/devices").iterdir():
        if attr(p, "idVendor") == "2e8a" and attr(p, "idProduct") in ("000a", "0003"):
            found.append((p.name, attr(p, "idProduct")))
    return found


def cdc():
    found = []
    for tty in Path("/sys/class/tty").glob("ttyACM*"):
        parent = (tty / "device").resolve()
        for p in (parent, *parent.parents):
            if p.name == USB_PORT and attr(p, "idVendor") == "2e8a" and attr(p, "idProduct") == "000a":
                found.append((tty.name, attr(p, "devnum")))
                break
    return found[0] if len(found) == 1 and pico_usb() == [(USB_PORT, "000a")] else None


def wait_for_cdc(previous=None):
    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        current = cdc()
        if current and (previous is None or current != previous):
            time.sleep(0.4)
            if cdc() == current:
                return current
        time.sleep(0.25)
    return None


def recover(previous):
    while True:
        print("\n復旧方法を選択してください:")
        print("  r: PicoのRESET/RUNボタンを押す（ない場合はUSB抜き差し）")
        print("  b: BOOTSELで起動し、既知の正常なT1ファームウェアを書き込む")
        print("  q: バッチを中止")
        choice = input("[r/b/q] > ").strip().lower()
        if choice == "q":
            return False
        if choice == "r":
            input("PicoだけをRESET/RUN（または抜き差し）し、操作後にEnter > ")
            current = wait_for_cdc(previous)
            if current:
                record(f"RESET後のCDC再列挙を確認: {current}")
                return True
            record("RESET後にCDC再列挙を確認できませんでした。BOOTSEL復旧を選べます。")
        elif choice == "b":
            print("PicoのUSBを抜き、BOOTSELを押しながら同じポートへ再接続してください。")
            input("BOOTSELにしたらEnter > ")
            if pico_usb() != [(USB_PORT, "0003")]:
                record(f"BOOTSEL確認失敗: 検出={pico_usb()}（接続位置や複数台を確認）")
                continue
            record("BOOTSELを確認。T1(tasks)を再書き込みしてCDCを復旧します。")
            cmd = ["tinygo", "flash", "-target=pico", "-scheduler=tasks", "t1/pico/main.go"]
            rc = subprocess.run(cmd, cwd=ROOT).returncode
            if rc != 0:
                record(f"BOOTSEL書き込み失敗 (exit={rc})。RPI-RP2のマウントも確認してください。")
                continue
            current = wait_for_cdc()
            if current:
                record(f"BOOTSELからCDCへの復旧を確認: {current}")
                return True
            record("書き込み後のCDC再列挙を確認できませんでした。")
        else:
            print("r、b、q のいずれかを入力してください。")


def main():
    results = []
    record(f"Batch start: {datetime.now().isoformat(timespec='seconds')}")
    record(f"Report: {REPORT}")
    try:
        for index, (test, sched, args) in enumerate(jobs, 1):
            name = f"{test} {sched} {args}".strip()
            attempt = 0
            while True:
                attempt += 1
                record(f"\n===== [{index}/{len(jobs)}] {name} (attempt {attempt}) =====")
                cmd = ["make", "test", f"TEST={test}", f"SCHED={sched}", "COUNT=1"]
                if args:
                    cmd.append(f"ARGS={args}")
                rc = subprocess.run(cmd, cwd=ROOT).returncode
                if rc == 0:
                    record(f"PASS: {name}")
                    results.append((name, "PASS"))
                    break

                record(f"FAILED: {name} (exit={rc})")
                print("\n★ まだRESETしないで、PicoのLEDを確認してください。")
                led = ""
                while not led:
                    led = input("LED状態 GP14/15/26/27/28（例: 28点灯固定、他消灯）> ").strip()
                record(f"LED observation: {name}: {led}")
                old = cdc()
                if not recover(old):
                    record("USER ABORT")
                    return 1
                while True:
                    choice = input("復旧済み。 [r]同じテスト再実行 / [s]失敗として次へ / [q]中止 > ").strip().lower()
                    if choice in ("r", "s", "q"):
                        break
                if choice == "r":
                    continue
                if choice == "s":
                    results.append((name, f"FAILED (exit={rc})"))
                    break
                record("USER ABORT")
                return 1
    except (KeyboardInterrupt, EOFError):
        record("INTERRUPTED: キーボード入力が中断されました。")
        return 130
    finally:
        record("\n===== BATCH SUMMARY =====")
        for name, outcome in results:
            record(f"{outcome:16} {name}")
        record(f"Report: {REPORT}")
    return 1 if any(outcome != "PASS" for _, outcome in results) else 0


if __name__ == "__main__":
    raise SystemExit(main())

