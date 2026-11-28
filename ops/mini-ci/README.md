# Mini build storage

Mini is for Mac builds and Mac-only CI.
Photo services remain on monolith.

- Runner software stays internal. Work moves to /Volumes/HDD-2TB/partyline-ci/work;
  caches, temporary files and logs use sibling directories. Only this new CI
  directory is managed. Existing HDD folders stay intact.
- One storage_guard.py runs as launcher (--launch) and through job-started.sh as the pre-job hook. GitHub requires a .sh, .ps1 or .js hook entrypoint. It checks the
  HDD UUID and mount, rejects symlink data paths and requires 25 GiB internally
  plus 100 GiB externally. Failures stop startup/jobs and attempt a bounded chat
  notification through existing SSH access. No HOME override.
- runner.env.example contains proposed non-secret settings. Merge these keys
  without displaying existing values. Set the work path in runner configuration;
  an environment setting does not change it.
- LaunchAgent retries startup with KeepAlive and ThrottleInterval=60 for late disk
  mounts. Redirect its logs externally.
- Weekly cleanup.py removes only files older than 14 days in cache, tmp and
  work/_temp, with no active Runner.Worker. Its kernel lock blocks new jobs during deletion.
  Checkouts, credentials and signed artifacts are retained. No quota or disk image.

Before enabling: review files, back up configuration and LaunchAgent, check idle,
stop only the Mini runner, copy/verify work data, configure paths, then restart.
Record moves and retain rollback copies. Validate guards, notification and actual
file destinations. Never detach the HDD during a job.

Xcode and the one installed simulator runtime stay internal. Native workflows
must explicitly send DerivedData, archives and simulator device data externally.
No native build until that is verified.

The storage workflow verifies real runner paths and records internal free space
before and after desktop tests. Cleanup runs Sunday at 04:00 Mini local time.
Alerts are sent once per failure episode through SSH pinned to monolith's directly
verified host key. Success clears the alert marker. Existing SSH settings stay intact.

Rollback restores saved configuration and LaunchAgent with the runner stopped.
Retain external data. Leave CI stopped if rollback would resume internal builds.
Never restart monolith services, tmux or sshd.

GitHub hooks: https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/run-scripts

## Netdata log retention

An hourly user LaunchAgent runs `netdata-logrotate.py`. Any of Netdata's six known
log files above 10 MiB is trimmed in place, retaining three compressed samples of
at most the last 10 MiB each. These are bounded diagnostic samples, not full log
history. Keeping the live inode avoids collectors continuing to write into renamed
or deleted archives. The threshold is checked hourly, rather than enforced per write.
No Netdata logs go onto the external CI drive. Maintenance failure sends one alert.

The one-time SSD cleanup retained a private 1 MiB collector-log sample and removed
unused npm download and Go build caches. It reclaimed about 48.3 GiB, leaving about
89 GiB free. Netdata was restarted once to close descriptors left by the initial
rename-based rotation test. Browser profiles, Docker, simulator data, Netdata's
metrics database and the CI rollback copies were retained.
