# v0.11.5 release note: isolated CLI state

The CLI now supports an explicit per-process config/session directory through
the global `--config-dir <absolute-path>` flag or `TTSBUDDY_CONFIG_DIR`.

The selected directory is used consistently for:

- `config.json` and all `config get/set` persistence;
- `last_job` recovery state;
- login, logout, and stored `ttsc_` CLI session state; and
- mutation and login locks.

The flag wins over the environment variable. Existing directories are reused;
missing directories are created on the first write. The CLI rejects relative,
empty, whitespace-only, filesystem-root, and regular-file paths and never falls
back to the default `~/.ttsbuddy` directory after an explicit isolation error.

## Verification matrix

| Case | Expected result |
| --- | --- |
| `TTSBUDDY_CONFIG_DIR=/tmp/agent-a ttsbuddy config set ...` | Writes only under `/tmp/agent-a` |
| `ttsbuddy --config-dir /tmp/agent-b config get ...` | Uses `/tmp/agent-b`, even if the environment points elsewhere |
| Relative, empty, whitespace-only, root, or regular-file path | Fails with a config-path error and does not touch `~/.ttsbuddy` |
| Session login/logout and `status` recovery | Reads and writes the isolated session/last-job records and locks |

Use v0.11.5 or later before relying on this option. Older releases reject the
unknown `--config-dir` flag and do not recognize `TTSBUDDY_CONFIG_DIR`.
