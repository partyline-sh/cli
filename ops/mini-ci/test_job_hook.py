import pathlib,subprocess,tempfile,unittest

class JobHookTests(unittest.TestCase):
 def test_wrapper_propagates_guard_result_from_path_with_spaces(self):
  source=pathlib.Path(__file__).with_name('job-started.sh')
  for code in [0,17]:
   with self.subTest(exit_code=code), tempfile.TemporaryDirectory(prefix='hook path ') as folder:
    root=pathlib.Path(folder);hook=root/'job-started.sh';hook.write_bytes(source.read_bytes())
    (root/'storage_guard.py').write_text('import sys; sys.exit('+str(code)+')\n')
    result=subprocess.run(['/bin/bash',str(hook)],capture_output=True)
    self.assertEqual(result.returncode,code)
 def test_config_uses_runner_supported_extension(self):
  root=pathlib.Path(__file__).parent
  lines=(root/'runner.env.example').read_text().splitlines()
  hook=next(x.split('=',1)[1] for x in lines if x.startswith('ACTIONS_RUNNER_HOOK_JOB_STARTED='))
  self.assertEqual(pathlib.Path(hook).suffix,'.sh')
  self.assertTrue((root/pathlib.Path(hook).name).is_file())

if __name__=='__main__':unittest.main()
