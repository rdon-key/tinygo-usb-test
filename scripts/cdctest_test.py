"""No hardware access: test sequencing, USB wait assertions, and metadata provenance."""
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import cdctest


class LauncherTest(unittest.TestCase):
    def setUp(self):
        self.target = dict(port='/dev/ttyACM7', usb='/sys/devices/usb/1-4', serial='abc', bus='1', address='5')

    def scenario(self, action, flash_fails=False, wait_fails=False, test='t1', args=''):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for case in sorted({'t0', 't1', test}):
                for kind in ('pc', 'pico'):
                    (root / case / kind).mkdir(parents=True)
                    (root / case / kind / 'main.go').write_text('package main')
            calls = []

            def command(_runner, argv, env=None):
                calls.append((argv, env))
                if argv[:2] == ['tinygo', 'flash'] and flash_fails:
                    raise subprocess.CalledProcessError(3, argv)

            before_cwd = Path.cwd()
            try:
                with patch.object(cdctest, 'ROOT', root), \
                     patch.dict(os.environ, {'CDCTEST_TEST': test, 'CDCTEST_SCHED': 'cores', 'CDCTEST_ARGS': args}, clear=True), \
                     patch.object(cdctest.subprocess, 'check_output', return_value='mock version'), \
                     patch.object(cdctest.Commands, 'run', command), \
                     patch.object(cdctest, 'flash_target', return_value=self.target), \
                     patch.object(cdctest, 'wait_cdc', side_effect=TimeoutError('mock timeout') if wait_fails else None,
                                  return_value=dict(self.target, port='/dev/ttyACM8', address='6')) as wait, \
                     contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                    code = cdctest.execute(action)
                metadata = json.loads(next(root.glob(test+'/results/*/metadata.json')).read_text())
            finally:
                os.chdir(before_cwd)
            return code, calls, metadata, wait.call_count

    def test_flash_failure_stops_before_wait_and_host(self):
        code, calls, metadata, waits = self.scenario('test', flash_fails=True)
        self.assertEqual(code, 3)
        self.assertEqual(waits, 0)
        self.assertNotIn('host_command', metadata)
        self.assertEqual(len(calls), 2)  # Go build, then failed flash.
        self.assertIn('-scheduler=cores', calls[-1][0])

    def test_enumeration_timeout_does_not_start_host(self):
        code, calls, metadata, waits = self.scenario('test', wait_fails=True)
        self.assertEqual(code, 1)
        self.assertEqual(waits, 1)
        self.assertNotIn('host_command', metadata)
        self.assertEqual(len(calls), 2)

    def test_success_records_flash_and_uses_rediscovered_port(self):
        code, calls, metadata, waits = self.scenario('test')
        self.assertEqual(code, 0)
        self.assertEqual(waits, 1)
        self.assertEqual(metadata['firmware']['scheduler'], 'cores')
        self.assertIn('/dev/ttyACM8', calls[-1][0])
        self.assertEqual(calls[-1][1]['CDCTEST_METADATA'].split('/')[-1], 'metadata.json')

    def test_run_never_assumes_firmware_scheduler(self):
        code, calls, metadata, waits = self.scenario('run')
        self.assertEqual(code, 0)
        self.assertEqual(waits, 0)
        self.assertEqual(metadata['firmware']['scheduler'], 'unknown')
        self.assertEqual(metadata['scheduler_requested'], 'cores')
        self.assertFalse(any(argv[:2] == ['tinygo', 'flash'] for argv, _ in calls))

    def test_wait_requires_new_address_at_same_connection(self):
        other = dict(self.target, usb='/sys/devices/usb/1-5', address='6')
        with patch.object(cdctest, 'cdc_devices', return_value=[self.target, other]), \
             patch.object(cdctest.time, 'sleep'):
            with self.assertRaises(TimeoutError):
                cdctest.wait_cdc(self.target, .001)

    def test_wait_accepts_stable_new_address_at_original_connection(self):
        after = dict(self.target, port='/dev/ttyACM8', address='6')
        ticks = iter(i / 10 for i in range(100))
        with patch.object(cdctest, 'cdc_devices', return_value=[after]), \
             patch.object(cdctest.time, 'monotonic', side_effect=lambda: next(ticks)), \
             patch.object(cdctest.time, 'sleep'):
            self.assertEqual(cdctest.wait_cdc(self.target, 5), after)

    def test_transition_case_arguments_and_metadata(self):
        code, calls, metadata, waits = self.scenario('test', test='t10',
                                                   args='-offsets 0s,50ms -off 750ms')
        self.assertEqual(code, 0)
        self.assertEqual(waits, 1)
        self.assertIn('-offsets', calls[-1][0])
        self.assertEqual(metadata['parameters']['offsets'], '0s,50ms')
        self.assertEqual(metadata['firmware']['scheduler'], 'cores')

    def test_flash_refuses_ambiguous_devices(self):
        with tempfile.TemporaryDirectory() as directory:
            usb = Path(directory)
            for name in ('1-1', '1-2'):
                device = usb / name
                device.mkdir()
                (device / 'idVendor').write_text('2e8a')
                (device / 'idProduct').write_text('000a')
            with patch.object(cdctest, 'USB', usb), patch.object(cdctest, 'cdc_devices', return_value=[self.target]):
                with self.assertRaises(RuntimeError):
                    cdctest.flash_target('')


class ParameterTest(unittest.TestCase):
    def test_unsupported_new_case_args_are_rejected_before_hardware(self):
        with patch.dict(os.environ, {'CDCTEST_TEST':'t2','CDCTEST_SCHED':'cores',
                                    'CDCTEST_ARGS':'-producers 4'}, clear=True):
            with self.assertRaises(ValueError):
                cdctest.execute('test')

    def test_unsupported_make_parameter_is_not_ignored(self):
        with patch.dict(os.environ, {'CDCTEST_TEST':'t7','CDCTEST_UNKNOWN_PARAMS':'UNKNOWN'}, clear=True):
            with self.assertRaises(ValueError):
                cdctest.execute('run')


if __name__ == '__main__':
    unittest.main()
