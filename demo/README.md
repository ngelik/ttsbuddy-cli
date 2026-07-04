# TTSBuddy CLI Demo

This demo is for the TTSBuddy CLI demo page at https://www.ttsbuddy.com/cli.

It uses a constrained no-signup endpoint:

```bash
export TTSBUDDY_API_URL=https://www.ttsbuddy.com/v1/cli-demo
export TTSBUDDY_API_KEY=ttsb_demo_cli
```

Demo mode accepts only these sample files plus the public CLI docs URL. It returns pregenerated MP3s so people can try the CLI without signup and without exposing a public arbitrary TTS generator.

`demo/cli-demo.sh` runs in constrained demo mode by default. It ignores inherited `TTSBUDDY_API_KEY` and `TTSBUDDY_API_URL`, uses the demo endpoint/key, and prints only a redacted key.

The pregenerated demo audio uses `af_heart` (Madison, American English) at `1.0x` speed.

## Run

```bash
./demo/cli-demo.sh
```

## Manual Commands

```bash
ttsbuddy speak -f demo/oncall-summary.md --voice af_heart --speed 1 -o out/oncall-summary.mp3
ttsbuddy speak -f demo/release-notes.md --voice af_heart --speed 1 --json
ttsbuddy web https://www.ttsbuddy.com/docs/developers/cli --voice af_heart --speed 1 --no-download
ttsbuddy speak -f demo/oncall-summary.md --voice af_heart --speed 1 -o - > out/oncall-summary-stdout.mp3
```

## Real API Mode

To intentionally test the script with a real key, run it with `TTSBUDDY_DEMO_USE_REAL_KEY=1` and an explicit `TTSBUDDY_API_KEY`. If `TTSBUDDY_API_URL` is not set, real-key mode defaults to `https://www.ttsbuddy.com/v1/agent-tts`.

```bash
TTSBUDDY_DEMO_USE_REAL_KEY=1 TTSBUDDY_API_KEY=ttsb_your_... ./demo/cli-demo.sh
```

API keys are created from the TTS Buddy dashboard. See https://www.ttsbuddy.com/docs/developers/api-keys.
