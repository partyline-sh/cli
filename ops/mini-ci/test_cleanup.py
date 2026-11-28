import os
import pathlib
import tempfile
import time
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import cleanup


class CleanupTests(unittest.TestCase):
    def test_only_old_managed_data_is_removed(self):
        with tempfile.TemporaryDirectory() as name:
            root = pathlib.Path(name).resolve()
            old = time.time() - 15 * 86400
            for relative in ('cache/old/item', 'cache/recent/item', 'tmp/old',
                             'work/project/source', 'artifacts/signed.ipa'):
                p = root / relative
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_text('retained data')
                os.utime(p, (old, old))
            os.utime(root / 'cache/old', (old, old))
            os.utime(root / 'cache/recent', (old, old))
            (root / 'cache/recent/item').touch()
            (root / 'cache/link').symlink_to(root / 'work/project', target_is_directory=True)
            (root / 'work/_temp').mkdir()
            old_temp = root / 'work/_temp/old'
            old_temp.write_text('temporary data')
            os.utime(old_temp, (0, 0))
            with patch.object(cleanup, 'ROOT', root), patch.object(cleanup, 'check'), \
                    patch.object(cleanup.subprocess, 'run', return_value=SimpleNamespace(returncode=1)):
                cleanup.main()
            self.assertFalse((root / 'cache/old').exists())
            self.assertFalse((root / 'tmp/old').exists())
            self.assertFalse(old_temp.exists())
            for relative in ('cache/recent/item', 'work/project/source', 'artifacts/signed.ipa', 'cache/link'):
                self.assertTrue((root / relative).exists(), relative)
            self.assertTrue((root / '.cleanup-lock').is_file())

    def test_active_or_unknown_worker_blocks_deletion(self):
        for status in (0, 2):
            with tempfile.TemporaryDirectory() as name:
                root = pathlib.Path(name).resolve()
                p = root / 'tmp/old'
                p.parent.mkdir()
                p.write_text('keep')
                os.utime(p, (0, 0))
                with patch.object(cleanup, 'ROOT', root), patch.object(cleanup, 'check'), \
                        patch.object(cleanup.subprocess, 'run', return_value=SimpleNamespace(returncode=status)):
                    cleanup.main()
                self.assertTrue(p.exists())


if __name__ == '__main__':
    unittest.main()
