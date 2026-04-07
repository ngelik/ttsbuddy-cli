# Manual Acceptance Plan for Installed ttsbuddy CLI

## Summary

Use the Homebrew-installed binary in an isolated test home so the test does not mutate the operator's real `~/.ttsbuddy` config. Validate both product behavior and README/help alignment.

## Setup

Use a dedicated shell session with isolated paths:

```bash
export TB_HOME="$(mktemp -d /tmp/ttsbuddy-manual.XXXXXX)"
export TB_OUT="$(mktemp -d /tmp/ttsbuddy-out.XXXXXX)"
alias tb='HOME="$TB_HOME" ttsbuddy'
```

Seed the test config explicitly:

```bash
tb config set key 'ttsb_<real_test_key>'
tb config set voice af_heart
tb config set speed 1.25
tb config set timeout 30s
tb config set output_dir "$TB_OUT"
```

Create markdown fixture:

```bash
cat > "$TB_OUT/test.md" <<'EOF'
# Test Document
This is **bold** with a [link](https://example.com).
The end.
EOF
```

For every test, capture separately:
- exit code
- stdout
- stderr
- created files, if any

Recommended wrapper pattern:

```bash
cmd='tb ...'
sh -c "$cmd" >case.stdout 2>case.stderr; echo $? >case.exit
```

## Test Matrix

### A. Command Surface, Help, and Docs

| # | Command | Expected | Exit |
|---|---------|----------|------|
| A.1 | `tb version` | Shows version, commit, date, Go version | 0 |
| A.2 | `tb version --json` | Valid JSON with version, commit, date, go | 0 |
| A.3 | `tb --version` | Same version information | 0 |
| A.4 | `tb --help` | Includes completion, config, speak, status, version, voices | 0 |
| A.5 | `tb speak --help` | Includes --file, --voice, --speed, --output, --output-dir, --timeout, --raw, --no-download, --idempotency-key | 0 |
| A.6 | `tb status --help`, `tb voices --help`, `tb config --help`, `tb completion --help` | Each exits 0 | 0 |
| A.7 | `tb completion zsh` | Non-empty shell script output | 0 |
| A.8 | **Documentation check only**: compare README examples/output-mode claims against actual behavior for `--json`, `-o -`, and `--no-download --json`. Record mismatches as doc defects. | — | — |

### B. Config and Precedence

| # | Command | Expected | Exit |
|---|---------|----------|------|
| B.1 | `tb config` | Resolved values shown; key redacted; speed shows `1.25`, not `1.2` | 0 |
| B.2 | `tb config --json` | Valid JSON; key redacted | 0 |
| B.3 | `tb config get key` | Redacted | 0 |
| B.4 | `tb config get voice` | `af_heart` | 0 |
| B.5 | `tb config get speed` | `1.25` | 0 |
| B.6 | `tb config get timeout` | `30s` | 0 |
| B.7 | `tb config set voice bf_emma` then `tb config get voice` | `bf_emma`; reset to `af_heart` | 0 |
| B.8 | `tb config set speed 0.9` then reset to `1.25` | Set and verified | 0 |
| B.9 | `tb config set speed 3.0` | Error: out of range | 2 |
| B.10 | `tb config set speed 1.0junk` | Error: invalid value | 2 |
| B.11 | `tb config set timeout garbage` | Error: invalid duration | 2 |
| B.12 | `tb config set bogus value` | Error: unknown key | 2 |
| B.13 | `tb config set key badformat` | Error: must start with `ttsb_` | 2 |
| B.14 | `tb config get nonexistent` | Error: unknown key | 2 |
| B.15 | `TTSBUDDY_VOICE=bf_emma tb config get voice` | `bf_emma` (env overrides config) | 0 |
| B.16 | Fresh-home smoke: with a new empty HOME, `ttsbuddy version`, `ttsbuddy --help`, `ttsbuddy voices`, and `ttsbuddy completion zsh` should still work without config | All exit 0 | 0 |

### C. Voices

| # | Command | Expected | Exit |
|---|---------|----------|------|
| C.1 | `tb voices` | Curated table with 23 rows; includes `af_heart` | 0 |
| C.2 | `tb voices --json` | Valid JSON array with 23 items | 0 |
| C.3 | `tb voices --all` | Live catalog or curated fallback. If live fetch succeeds, valid table with >= 23 voices. Do not assert a fixed live count. | 0 |
| C.4 | `tb voices --all --json` | Valid JSON; no stderr noise in JSON mode | 0 |

### D. Non-POST Input Validation

| # | Command | Expected | Exit |
|---|---------|----------|------|
| D.1 | `tb speak` | Error: no input provided | 2 |
| D.2 | `tb speak --json -o - "test"` | Error: stdout mode conflict | 2 |
| D.3 | `tb speak -f /nonexistent` | Error: file not found | 2 |
| D.4 | `tb speak -f /tmp` | Error: not a regular file | 2 |

## POST Test Sequence

Every command below is a POST and must be separated by ~65 seconds. Reuse outputs instead of adding extra POSTs.

| # | Command | Expected | Exit |
|---|---------|----------|------|
| P.1 | `tb speak "File save smoke" -o "$TB_OUT/t1.mp3"` | Stderr includes "Saved to"; file exists and non-empty | 0 |
| P.2 | `tb speak "JSON smoke" --json > "$TB_OUT/t2.json"` | Valid JSON with status, job_id, audio_url. **Record job_id for status tests.** | 0 |
| P.3 | `tb speak "URL only" --no-download` | URL emitted; no file created. Note whether output is on stderr and record behavior. | 0 |
| P.4 | `tb speak "Quiet smoke" -o "$TB_OUT/t4.mp3" --quiet` | File exists; no human progress/status chatter | 0 |
| P.5 | `printf 'Piped text\n' \| tb speak - -o "$TB_OUT/t5.mp3"` | Stdin path works | 0 |
| P.6 | `tb speak -f "$TB_OUT/test.md" -o "$TB_OUT/t6.mp3"` | Markdown input path works | 0 |
| P.7 | `tb speak -f "$TB_OUT/test.md" --raw -o "$TB_OUT/t7.mp3"` | Raw markdown path works | 0 |
| P.8 | `tb speak "Stdout smoke" -o - > "$TB_OUT/t8.mp3"` | Stdout contains playable MP3 bytes; file non-empty | 0 |
| P.9 | `tb speak "Voice speed idem" -v bf_emma -s 0.7 --idempotency-key manual-test-001 -o "$TB_OUT/t9.mp3"` | Custom voice, speed, and explicit idempotency key | 0 |
| P.10 | `tb speak "Auto-name smoke" --output-dir "$TB_OUT"` | Auto-named file created matching `ttsbuddy-YYYYMMDD-HHMMSS-<voice>.mp3` | 0 |

**Optional POST regressions** (only if there is time/rate budget):
- `tb speak "bad key" -k ttsb_bad_key` → exit 1; human-readable auth error
- `tb speak "bad key" --json -k ttsb_bad_key` → exit 1; JSON error on stdout only

## Status and Output Verification

Use the job_id captured from test P.2:

| # | Command | Expected | Exit |
|---|---------|----------|------|
| S.1 | `tb status` | Uses saved last job | 0 |
| S.2 | `tb status --json` | Valid JSON | 0 |
| S.3 | `tb status <job_id>` | Shows specific job status | 0 |
| S.4 | `tb status <job_id> --watch --timeout 30s` | Exit 0 for completed job; no polling error | 0 |
| S.5 | `tb status 00000000-0000-0000-0000-000000000000` | Not found | 1 |

**Artifact verification:**
- `ls -lh "$TB_OUT"` shows expected output files
- `file "$TB_OUT/t1.mp3" "$TB_OUT/t8.mp3"` identifies audio/MP3 data
- Auto-named output exists inside `$TB_OUT`, not outside it
- No unexpected file is created for `--no-download`

## Assumptions and Release Gates

**Assumptions:**
- Tests run against production with a key subject to rate limiting
- Live voice catalog size is unstable; curated list size is stable at 23
- `--json` is the authoritative machine-output mode; combined-mode behavior that contradicts README should be recorded

**Release gates:**
- Any exit-code mismatch is a **failure**
- Any documented command/flag missing from help is a **failure**
- Any README/help mismatch is a **doc failure**, even if binary behavior is acceptable
- Any POST test that requires a retry because of rate limit should be **rerun after waiting**, not marked failed

## Cleanup

```bash
rm -rf "$TB_HOME" "$TB_OUT"
```
