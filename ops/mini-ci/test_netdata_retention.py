import gzip
import importlib.util
import os
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('retention', pathlib.Path(__file__).with_name('netdata-logrotate.py'))
retention = importlib.util.module_from_spec(spec)
spec.loader.exec_module(retention)


class LogRetentionTests(unittest.TestCase):
    def test_open_writer_keeps_same_inode_and_samples_are_bounded(self):
        with tempfile.TemporaryDirectory() as directory:
            log = pathlib.Path(directory) / 'collector.log'
            with log.open('ab', buffering=0) as writer:
                inode = os.fstat(writer.fileno()).st_ino
                for n in range(5):
                    writer.write(bytes([65 + n]) * 1024)
                    self.assertTrue(retention.trim(log, limit=128))
                    self.assertEqual(log.stat().st_ino, inode)
                    self.assertEqual(log.stat().st_size, 0)
                    self.assertEqual(gzip.decompress(log.with_name(log.name + '.partyline-1.gz').read_bytes()), bytes([65 + n]) * 128)
                writer.write(b'next event')
                self.assertEqual(log.read_bytes(), b'next event')
                self.assertEqual(len(list(log.parent.glob('*.gz'))), 3)

    def test_small_logs_retained_and_symlink_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            log = pathlib.Path(directory) / 'collector.log'
            log.write_bytes(b'keep')
            self.assertFalse(retention.trim(log, limit=128))
            self.assertEqual(log.read_bytes(), b'keep')
            link = log.with_name('link')
            link.symlink_to(log)
            with self.assertRaises(OSError):
                retention.trim(link, limit=1)


if __name__ == '__main__':
    unittest.main()
