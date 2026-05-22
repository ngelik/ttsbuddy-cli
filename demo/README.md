# TTSBuddy CLI Demo

This demo is for the TTSBuddy CLI demo page at https://www.ttsbuddy.com/cli.

It uses a constrained no-signup endpoint:

```bash
export TTSBUDDY_API_URL=https://www.ttsbuddy.com/v1/cli-demo
export TTSBUDDY_API_KEY=ttsb_demo_cli
```

Demo mode accepts only these sample files plus the public CLI docs URL. It returns pregenerated MP3s so people can try the CLI without signup and without exposing a public arbitrary TTS generator.

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

For arbitrary text, unset the demo API URL and configure a real key:

```bash
unset TTSBUDDY_API_URL
ttsbuddy config set key ttsb_your_key_here
```

API keys are created from the TTS Buddy dashboard. See https://www.ttsbuddy.com/docs/developers/api-keys.
