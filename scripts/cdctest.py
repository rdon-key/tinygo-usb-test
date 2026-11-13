"""Small Linux USB CDC launcher. No USB writes except explicit flash/test/run actions."""
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shlex
import signal
import stat
import subprocess
import sys
import tempfile
import time
from datetime import datetime

ROOT = Path(__file__).resolve().parents[1]
USB = Path('/sys/bus/usb/devices')
TTY = Path('/sys/class/tty')


def now():
    return datetime.now().astimezone().isoformat(timespec='microseconds')


def read(path):
    try:
        return path.read_text().strip()
    except OSError:
        return ''


def cdc_devices():
    devices = []
    for tty in sorted(TTY.glob('ttyACM*')):
        parent = (tty / 'device').resolve()
        for usb in parent.parents:
            if read(usb / 'idVendor'):
                port = Path('/dev') / tty.name
                if (read(usb / 'idVendor'), read(usb / 'idProduct')) == ('2e8a', '000a'):
                    try:
                        if stat.S_ISCHR(port.stat().st_mode):
                            devices.append(dict(port=str(port), usb=str(usb),
                                                serial=read(usb / 'serial'),
                                                bus=read(usb / 'busnum'), address=read(usb / 'devnum')))
                    except OSError:
                        pass
                break
    return devices


def flash_target(port):
    # TinyGo's BOOTSEL volume search must not encounter a second Pico.
    boards = [p for p in USB.iterdir() if read(p / 'idVendor') == '2e8a'
              and read(p / 'idProduct') in ('000a', '0003')]
    devices = cdc_devices()
    if len(boards) != 1 or len(devices) != 1:
        raise RuntimeError('Flash requires exactly one Pico (2e8a:000a) with an accessible CDC node, '
                           'and no other Pico/BOOTSEL device. Use manual BOOTSEL setup for initial flashing.')
    target = devices[0]
    if port and str(Path(port).resolve()) != target['port']:
        raise RuntimeError(f"PORT does not identify the sole Pico CDC device: {target['port']}")
    return target


def wait_cdc(before, seconds):
    deadline = time.monotonic() + seconds
    stable = None
    since = time.monotonic()
    while time.monotonic() < deadline:
        matches = [d for d in cdc_devices() if d['usb'] == before['usb']
                   and (not before['serial'] or d['serial'] == before['serial'])
                   and (d['bus'], d['address']) != (before['bus'], before['address'])]
        candidate = matches[0] if len(matches) == 1 else None
        if candidate != stable:
            stable, since = candidate, time.monotonic()
        if stable and time.monotonic() - since >= 0.3:
            return stable
        time.sleep(min(0.1, max(0, deadline - time.monotonic())))
    raise TimeoutError('CDC re-enumeration timeout: no new USB address and stable CDC node '
                       'at the original USB connection. Host test was not started.')


class Commands:
    def __init__(self, log):
        self.log = log
        self.child = None

    def interrupt(self, sig, _frame):
        if self.child is None:
            raise KeyboardInterrupt
        try:
            os.killpg(self.child.pid, sig)
        except ProcessLookupError:
            pass

    def run(self, argv, env=None):
        line = '$ ' + shlex.join(map(str, argv))
        print(line, flush=True)
        self.log.write(line + '\n')
        self.log.flush()
        with subprocess.Popen(argv, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                              text=True, errors='replace', start_new_session=True) as child:
            self.child = child
            try:
                for line in child.stdout:
                    print(line, end='', flush=True)
                    self.log.write(line)
                    self.log.flush()
                code = child.wait()
            finally:
                self.child = None
        if code:
            raise subprocess.CalledProcessError(128 - code if code < 0 else code, argv)


def execute(action):
    os.chdir(ROOT)
    if action == 'help':
        print('''USB CDC test management (run from any directory using make -f /path/to/Makefile)
  make build TEST=t0..t11 SCHED=tasks|cores   Build host and Pico UF2 (no flash)
  make flash TEST=t0..t11 SCHED=tasks|cores   Flash the sole verified Pico CDC device
  make run TEST=t0..t11 COUNT=N PORT=...     Run host only; firmware configuration UNKNOWN
  make test TEST=t0..t11 SCHED=tasks|cores   Flash, wait for CDC re-enumeration, run host
  make check                             Build/vet/test all hosts and check launcher
Options: COUNT=1(t0)/100(t1..t11), 0=unlimited; PORT=<empty for auto>; HOLD=1s (t0);
         TIMEOUT=5s INTERVAL=100ms (t1..t11); ENUM_TIMEOUT=20 (seconds); ARGS="-test-specific-option value" (see case README)
Default: TEST=t1 SCHED=tasks. flash/test/run access hardware; build/check/help do not.
Artifacts: build/<test>-<scheduler>.uf2; <test>/results/<unique-run>/metadata.json,
           console.log, results.csv (when host starts). See README.md for limitations.''')
        return 0
    test = os.environ.get('CDCTEST_TEST', 't1')
    scheduler = os.environ.get('CDCTEST_SCHED', 'tasks')
    if test not in tuple('t'+str(i) for i in range(12)) or scheduler not in ('tasks', 'cores'):
        raise ValueError('TEST must be t0..t11 and SCHED must be tasks/cores')
    count = os.environ.get('CDCTEST_COUNT', '1' if test == 't0' else '100')
    if not count.isascii() or not count.isdecimal() or int(count) > 2**64 - 1:
        raise ValueError('COUNT must be an unsigned 64-bit integer; 0 means unlimited')
    enum_timeout = float(os.environ.get('CDCTEST_ENUM_TIMEOUT', '20'))
    if not 0 < enum_timeout <= 3600:
        raise ValueError('ENUM_TIMEOUT must be positive and at most 3600 seconds')
    port = os.environ.get('CDCTEST_PORT', '')
    params = dict(count=count, port=port, hold=os.environ.get('CDCTEST_HOLD', '1s')) if test == 't0' else dict(
        count=count, port=port, timeout=os.environ.get('CDCTEST_TIMEOUT', '5s'),
        interval=os.environ.get('CDCTEST_INTERVAL', '100ms'))
    if test not in ('t0', 't1') and os.environ.get('CDCTEST_UNKNOWN_PARAMS', '').strip():
        raise ValueError('Unsupported Make parameters: ' + os.environ['CDCTEST_UNKNOWN_PARAMS'])
    if test not in ('t0', 't1') and 'HOLD' in os.environ.get('CDCTEST_EXPLICIT_PARAMS', '').split():
        raise ValueError('HOLD is only supported by T0; use the documented test-specific ARGS')
    allowed = {'t2': {'lengths'}, 't3': {'burst', 'window', 'request-interval'}, 't4': {'frames'},
               't5': {'frames', 'producers', 'mode'},
               **{f't{i}': {'offsets', 'off', 'reconnect'} for i in range(7,11)},
               't11': {'power-config', 'off', 'reconnect'}}.get(test, set())
    extra = shlex.split(os.environ.get('CDCTEST_ARGS', ''))
    while extra:
        item = extra.pop(0)
        key, equal, value = item.lstrip('-').partition('=')
        if not item.startswith('-') or key not in allowed or key in params:
            raise ValueError(f'Unsupported or duplicate parameter for {test}: {item}')
        if not equal:
            if not extra:
                raise ValueError(f'Missing value for {item}')
            value = extra.pop(0)
        params[key] = value
    directory = ROOT / test / 'results'
    directory.mkdir(parents=True, exist_ok=True)
    run_dir = Path(tempfile.mkdtemp(prefix=datetime.now().strftime('%Y%m%dT%H%M%S-'), dir=directory))
    meta_path = run_dir / 'metadata.json'
    metadata = dict(run_id=run_dir.name, action=action, test=test, start=now(),
                    host=platform.platform(), target_requested='pico', scheduler_requested=scheduler,
                    parameters=params, enumeration_timeout_seconds=enum_timeout,
                    firmware=dict(status='unknown', target='unknown', scheduler='unknown', tinygo_version='unknown'),
                    source_sha256={str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
                                   for p in (ROOT / test / 'pico/main.go', ROOT / test / 'pc/main.go')},
                    csv=str(run_dir / 'results.csv'))

    def save():
        # Same directory, atomically replaced; CSV holds the host-start snapshot.
        temp = meta_path.with_suffix('.tmp')
        temp.write_text(json.dumps(metadata, ensure_ascii=True) + '\n')
        temp.replace(meta_path)

    save()
    begin = time.monotonic()
    exit_code = 1
    with (run_dir / 'console.log').open('x') as log:
        commands = Commands(log)
        old_handlers = {sig: signal.signal(sig, commands.interrupt) for sig in (signal.SIGINT, signal.SIGTERM)}
        try:
            metadata['tinygo_version'] = subprocess.check_output(['tinygo', 'version'], text=True, timeout=10).strip()
            metadata['llvm_version'] = re.search(r'LLVM version ([^)]+)', metadata['tinygo_version']).group(1) if 'LLVM version ' in metadata['tinygo_version'] else 'unknown'
            metadata['go_version'] = subprocess.check_output(['go', 'version'], text=True, timeout=10).strip()
            save()
            print('Run records:', run_dir, flush=True)
            build = ROOT / 'build'
            build.mkdir(exist_ok=True)
            host = run_dir / f'{test}-pc'
            if action == 'check':
                for case in ('t'+str(i) for i in range(12)):
                    source = f'{case}/pc/main.go'
                    commands.run(['go', 'build', '-o', str(run_dir / f'{case}-pc'), source])
                    commands.run(['go', 'vet', source])
                    commands.run(['go', 'test', '-v', source, *map(str, sorted(Path(case, 'pc').glob('*_test.go')))])
                commands.run([sys.executable, '-m', 'unittest', 'discover', '-s', 'scripts', '-p', '*_test.py'])
            else:
                if action in ('build', 'run', 'test'):
                    commands.run(['go', 'build', '-o', str(host), f'{test}/pc/main.go'])
                if action == 'build':
                    commands.run(['tinygo', 'build', '-target=pico', f'-scheduler={scheduler}',
                                  '-o', str(build / f'{test}-{scheduler}.uf2'), f'{test}/pico/main.go'])
                if action in ('flash', 'test'):
                    before = flash_target(port)
                    metadata['usb_before_flash'] = before
                    argv = ['tinygo', 'flash', '-target=pico', f'-scheduler={scheduler}',
                            f"-port={before['port']}", f'{test}/pico/main.go']
                    metadata['flash_command'] = argv
                    metadata['flash_start'] = now()
                    metadata['firmware']['status'] = 'flash_attempted'
                    save()
                    commands.run(argv)  # Failure raises: never fall through to wait or host.
                    metadata['flash_end'] = now()
                    metadata['firmware'] = dict(status='flash_command_succeeded_not_readback_verified',
                                               target='pico', scheduler=scheduler,
                                               tinygo_version=metadata['tinygo_version'])
                    save()
                    if action == 'test':
                        print('Waiting for USB re-enumeration and CDC port creation...', flush=True)
                        metadata['enumeration_start'] = now()
                        save()
                        after = wait_cdc(before, enum_timeout)
                        metadata['enumeration_end'] = now()
                        metadata['usb_after_flash'] = after
                        params['port'] = after['port']
                if action in ('run', 'test'):
                    argv = [str(host)]
                    for key, value in params.items():
                        if value != '':
                            argv.extend(['-' + key, value])
                    if test != 't0':
                        argv.extend(['-csv', metadata['csv']])
                    metadata['host_command'] = argv
                    metadata['host_start'] = now()
                    save()
                    env = dict(os.environ, CDCTEST_METADATA=str(meta_path), CDCTEST_CSV=metadata['csv'])
                    commands.run(argv, env)
            exit_code = 0
        except KeyboardInterrupt:
            exit_code = 130
            metadata['error'] = 'Interrupted before host completion'
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            exit_code = error.returncode if isinstance(error, subprocess.CalledProcessError) else 1
            metadata['error'] = str(error)
            print('Error:', error, file=sys.stderr)
            log.write('Error: ' + str(error) + '\n')
        finally:
            metadata.update(end=now(), elapsed_seconds=time.monotonic() - begin, exit_code=exit_code)
            save()
            for sig, handler in old_handlers.items():
                signal.signal(sig, handler)
    return exit_code


if __name__ == '__main__':
    try:
        if len(sys.argv) != 2 or sys.argv[1] not in ('help', 'build', 'flash', 'run', 'test', 'check'):
            raise ValueError('Expected help/build/flash/run/test/check')
        sys.exit(execute(sys.argv[1]))
    except (OSError, ValueError) as error:
        print('Error:', error, file=sys.stderr)
        sys.exit(2)
