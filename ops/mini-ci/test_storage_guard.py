import copy
import pathlib
import tempfile
import unittest
from collections import namedtuple
from unittest.mock import patch

import storage_guard as guard

Space = namedtuple('Space', 'total used free')


class StorageGuardTests(unittest.TestCase):
    def setUp(self):
        self.drive = {'VolumeUUID': guard.HDD_UUID, 'MountPoint': str(guard.HDD)}
        self.spaces = {
            '/': Space(228 * guard.GIB, 188 * guard.GIB, 40 * guard.GIB),
            str(guard.HDD): Space(1800 * guard.GIB, 300 * guard.GIB, 1500 * guard.GIB),
        }

    def info(self, command):
        return copy.deepcopy(self.drive)

    def check(self):
        guard.check(self.info, self.spaces.__getitem__)

    def test_correct_storage_passes(self):
        self.check()

    def test_wrong_or_missing_hdd_fails(self):
        for drive in [{}, {**self.drive, 'VolumeUUID': 'other'},
                      {**self.drive, 'MountPoint': '/'}]:
            self.drive = drive
            with self.assertRaises(guard.StorageError):
                self.check()

    def test_low_space_fails_on_each_filesystem(self):
        for location in self.spaces:
            with self.subTest(location=location):
                original = self.spaces[location]
                self.spaces[location] = original._replace(free=guard.GIB)
                with self.assertRaises(guard.StorageError):
                    self.check()
                self.spaces[location] = original

    def test_symlinked_storage_fails(self):
        for target in [guard.ROOT]:
            with self.subTest(target=target):
                with patch.object(pathlib.Path, 'resolve', autospec=True,
                                  side_effect=lambda p: pathlib.Path('/wrong') if p == target else p):
                    with self.assertRaises(guard.StorageError):
                        self.check()

    def test_notification_is_once_until_success(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = pathlib.Path(directory) / 'notified'
            with patch.object(guard, 'MARKER', marker), patch.object(guard.subprocess, 'run') as send:
                guard.notify_once('fixed test message')
                guard.notify_once('fixed test message')
                self.assertEqual(send.call_count, 1)
                marker.unlink()
                guard.notify_once('fixed test message')
                self.assertEqual(send.call_count, 2)

    def test_missing_mount_fails_before_space_checks(self):
        self.drive = {}
        with patch.object(guard.shutil, 'disk_usage', side_effect=AssertionError('must not inspect fallback')):
            with self.assertRaises(guard.StorageError):
                guard.check(info=self.info)

    def test_cleanup_can_run_below_space_reserve(self):
        guard.check(info=self.info, usage=lambda _: (_ for _ in ()).throw(AssertionError()), reserves=False)


if __name__ == '__main__':
    unittest.main()
