from __future__ import annotations

import json
import importlib
import importlib.util
import os
import tempfile
import textwrap
import unittest
from contextlib import contextmanager
from contextvars import ContextVar
from pathlib import Path
from unittest import mock

from agent.secret_scope import reset_secret_scope, set_secret_scope

PLUGIN_DIR = Path(__file__).resolve().parents[1]
import sys
spec = importlib.util.spec_from_file_location(
    "hermes_ttsbuddy",
    PLUGIN_DIR / "__init__.py",
    submodule_search_locations=[str(PLUGIN_DIR)],
)
plugin = importlib.util.module_from_spec(spec)
sys.modules["hermes_ttsbuddy"] = plugin
spec.loader.exec_module(plugin)
provider_module = importlib.import_module("hermes_ttsbuddy.provider")
TTSBuddyError = provider_module.TTSBuddyError
TTSBuddyProvider = provider_module.TTSBuddyProvider
register = plugin.register


_ACTIVE_PROFILE: ContextVar[str] = ContextVar("ttsbuddy_test_profile", default="default")


class FakeContext:
    def __init__(self, settings: dict | None = None, profiles: dict | None = None) -> None:
        self.settings = settings or {}
        self.profiles = profiles or {}
        self.registered = []
        self.tools = []

    def get_config(self, key, default=None):
        selected = self.profiles.get(_ACTIVE_PROFILE.get(), self.settings)
        return selected.get(key, default)

    def register_tts_provider(self, provider):
        self.registered.append(provider)

    def register_tool(self, **tool):
        self.tools.append(tool)


@contextmanager
def profile_scope(name: str, secrets: dict[str, str]):
    profile_token = _ACTIVE_PROFILE.set(name)
    secret_token = set_secret_scope(secrets, profile_home=f"/tmp/ttsbuddy-profile-{name}")
    try:
        yield
    finally:
        reset_secret_scope(secret_token)
        _ACTIVE_PROFILE.reset(profile_token)


FAKE_CLI = r'''#!/usr/bin/env python3
import json
import os
import pathlib
import sys
import time

argv = sys.argv[1:]
record = os.environ.get("FAKE_RECORD")
if record:
    with open(record, "a", encoding="utf-8") as stream:
        stream.write(json.dumps({"argv": argv, "env": dict(os.environ), "stdin": sys.stdin.read() if "speak" in argv else ""}) + "\n")
elif "speak" in argv:
    text = sys.stdin.read()
else:
    text = ""

if "doctor" in argv:
    print(json.dumps({"ready": os.environ.get("FAKE_DOCTOR_READY", "yes") == "yes",
                      "checks": [{"name": "credential", "status": "healthy"}]}))
    sys.exit(0 if os.environ.get("FAKE_DOCTOR_READY", "yes") == "yes" else 1)

if "voices" in argv:
    if "--all" in argv:
        print("unexpected online catalog", file=sys.stderr)
        sys.exit(3)
    print(json.dumps([
        {"id": "af_heart", "name": "Madison", "language": "American English", "gender": "Female"},
        {"id": "st_f1", "name": "Ava", "language": "French", "gender": "Female"},
    ]))
    sys.exit(0)

mode = os.environ.get("FAKE_MODE", "success")
if "download" in argv:
    job_id = argv[argv.index("download") + 1]
    output = pathlib.Path(argv[argv.index("--output") + 1])
    if mode == "download_timeout":
        output.write_bytes(b"partial")
        time.sleep(5)
        sys.exit(0)
    if mode == "download_invalid_json":
        print("{not-json")
        sys.exit(0)
    if mode == "download_error":
        print(json.dumps({"success": False, "error": {
            "code": "CLI_ERROR", "server_code": "TTS_PROVIDER_ERROR",
            "action": {"type": "download", "argv": ["ttsbuddy", "download", "job_other123"]},
            "message": "SECRET-DO-NOT-RETURN https://signed.example.invalid/audio?sig=secret",
        }}))
        print("SECRET-STDERR https://signed.example.invalid", file=sys.stderr)
        sys.exit(1)
    if mode != "download_missing_file":
        output.write_bytes(b"ID3-test-audio")
    reported_path = os.path.abspath(str(output))
    if mode == "download_wrong_path":
        reported_path += ".other"
    payload = {
        "success": True,
        "status": "completed",
        "job_id": job_id,
        "audio": {"format": "mp3", "voice": "af_heart"},
        "download": {
            "path": reported_path,
            "bytes": 1 if mode == "download_wrong_bytes" else (output.stat().st_size if output.exists() else 0),
        },
    }
    print(json.dumps(payload))
    sys.exit(0)

if "speak" not in argv:
    sys.exit(4)

if mode == "slow":
    output = argv[argv.index("--output") + 1]
    pathlib.Path(output).write_bytes(b"partial")
    time.sleep(5)
    sys.exit(0)

idempotency_key = argv[argv.index("--idempotency-key") + 1]
if mode in ("auth", "quota", "recovery"):
    server_code = {"auth": "AUTH_REQUIRED", "quota": "USAGE_LIMIT_EXCEEDED", "recovery": "POLL_TIMEOUT"}[mode]
    detail = {
        "code": "CLI_ERROR",
        "server_code": server_code,
        "message": "SECRET-DO-NOT-RETURN https://signed.example.invalid/audio?signature=secret",
        "idempotency_key": idempotency_key,
    }
    if mode == "recovery":
        detail["action"] = {"type": "download", "argv": ["ttsbuddy", "download", "job_abc123", "--json"]}
    print(json.dumps({"success": False, "error": detail}))
    print("SECRET-STDERR https://signed.example.invalid", file=sys.stderr)
    sys.exit(1)
if mode == "invalid_json":
    print("{not-json")
    sys.exit(1)

output = pathlib.Path(argv[argv.index("--output") + 1])
if mode not in ("missing_file",):
    output.write_bytes(b"ID3-test-audio")
reported_path = os.path.abspath(str(output))
if mode == "wrong_path":
    reported_path += ".other"
size = 1 if mode in ("wrong_bytes", "missing_file") else (output.stat().st_size if output.exists() else 0)
payload = {
    "success": True,
    "status": "completed",
    "job_id": "job_success123",
    "audio": {"format": "mp3", "voice": "af_heart"},
    "download": {"path": reported_path, "bytes": size},
}
if mode == "missing_metadata":
    payload.pop("download")
if mode == "wrong_format":
    payload["audio"]["format"] = "wav"
if mode == "invalid_success_json":
    print("[]")
else:
    print(json.dumps(payload))
'''


class ProviderTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.record = self.root / "calls.jsonl"
        self.record.write_text("", encoding="utf-8")
        self.hermes_home = self.root / "hermes-home"
        self.hermes_home.mkdir()
        self.cli = self.root / "fake-ttsbuddy"
        self.cli.write_text(textwrap.dedent(FAKE_CLI), encoding="utf-8")
        self.cli.chmod(0o755)
        self.settings = {"cli_path": str(self.cli), "poll_timeout_seconds": 30}
        self.ctx = FakeContext(self.settings)
        self.provider = TTSBuddyProvider(self.ctx)

    def tearDown(self):
        self.temp.cleanup()

    def _call_env(self, **extra):
        env = {"FAKE_RECORD": str(self.record), "HERMES_HOME": str(self.hermes_home)}
        env.update(extra)
        return mock.patch.dict(os.environ, env, clear=False)

    def _calls(self):
        if not self.record.exists():
            return []
        return [json.loads(line) for line in self.record.read_text(encoding="utf-8").splitlines()]

    def _synthesize(self, *, path: Path | None = None, **kwargs):
        target = path or self.root / "speech.wav"
        return self.provider.synthesize("Hello from Hermes", str(target), **kwargs)

    def test_register_uses_native_tts_provider_api(self):
        context = FakeContext(self.settings)
        register(context)
        self.assertEqual(len(context.registered), 1)
        self.assertIsInstance(context.registered[0], TTSBuddyProvider)
        self.assertEqual(context.registered[0].name, "ttsbuddy")
        self.assertTrue(context.registered[0].voice_compatible)
        self.assertFalse(context.registered[0].streams_pcm)
        self.assertEqual(len(context.tools), 1)
        self.assertEqual(context.tools[0]["name"], "ttsbuddy_recover")
        self.assertEqual(context.tools[0]["toolset"], "tts")
        self.assertEqual(context.tools[0]["schema"]["parameters"]["required"], ["job_id", "output_path"])

    def test_success_uses_stdin_json_mp3_agent_context_and_scoped_secret(self):
        with self._call_env(
            FAKE_MODE="success",
            TTSBUDDY_API_KEY="ambient-key-must-not-leak",
            TTSBUDDY_API_URL="https://demo.example.invalid/v1/cli-demo",
            TTSBUDDY_CONFIG_DIR="/ambient/config",
            TTSBUDDY_EXECUTION_CONTEXT="robot",
            TTSBUDDY_TTS_API_BASE_URL="https://demo.example.invalid",
        ), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_profile_alpha"}):
            output = self._synthesize(voice="af_heart", speed=0.9, format="opus")

        self.assertEqual(output, str((self.root / "speech.mp3").absolute()))
        self.assertEqual(Path(output).read_bytes(), b"ID3-test-audio")
        calls = self._calls()
        self.assertEqual(len(calls), 1, "a failed/uncertain synthesis must not trigger an outer retry")
        call = calls[0]
        argv = call["argv"]
        self.assertEqual(call["stdin"], "Hello from Hermes")
        self.assertIn("speak", argv)
        self.assertIn("-", argv)
        self.assertIn("--output", argv)
        self.assertIn("--json", argv)
        self.assertEqual(argv[argv.index("--execution-context") + 1], "agent")
        self.assertEqual(argv[argv.index("--voice") + 1], "af_heart")
        self.assertEqual(argv[argv.index("--speed") + 1], "0.9")
        self.assertEqual(argv[argv.index("--timeout") + 1], "30s")
        self.assertRegex(argv[argv.index("--idempotency-key") + 1], provider_module._SAFE_IDEMPOTENCY_KEY)
        self.assertTrue(argv[argv.index("--output") + 1].endswith("audio.mp3"))
        self.assertEqual(call["env"]["TTSBUDDY_API_KEY"], "ttsb_profile_alpha")
        self.assertNotIn("TTSBUDDY_API_URL", call["env"])
        self.assertNotIn("TTSBUDDY_CONFIG_DIR", call["env"])
        self.assertNotIn("TTSBUDDY_EXECUTION_CONTEXT", call["env"])
        self.assertNotIn("TTSBUDDY_TTS_API_BASE_URL", call["env"])

    def test_same_provider_resolves_profile_secret_and_settings_for_each_request(self):
        self.ctx.profiles = {
            "alpha": {"cli_path": str(self.cli), "poll_timeout_seconds": 30},
            "beta": {"cli_path": str(self.cli), "poll_timeout_seconds": 45},
        }
        with self._call_env(FAKE_MODE="success"):
            with profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
                first = self._synthesize(path=self.root / "alpha.ogg")
            with profile_scope("beta", {"TTSBUDDY_API_KEY": "ttsb_beta"}):
                second = self._synthesize(path=self.root / "beta.flac")

        self.assertTrue(first.endswith("alpha.mp3"))
        self.assertTrue(second.endswith("beta.mp3"))
        calls = self._calls()
        self.assertEqual(len(calls), 2)
        self.assertEqual([call["env"]["TTSBUDDY_API_KEY"] for call in calls], ["ttsb_alpha", "ttsb_beta"])
        config_dirs = [call["argv"][call["argv"].index("--config-dir") + 1] for call in calls]
        self.assertNotEqual(config_dirs[0], config_dirs[1])
        self.assertNotIn("cli_config_dir", self.ctx.settings)

    def test_explicit_cli_config_route_clears_ambient_credential_and_endpoint(self):
        selected_config = self.root / "selected-cli-config"
        selected_config.mkdir(mode=0o700)
        self.ctx.settings["cli_config_dir"] = str(selected_config)
        with self._call_env(
            FAKE_MODE="success",
            TTSBUDDY_API_KEY="ambient-key-must-not-leak",
            TTSBUDDY_API_URL="https://demo.example.invalid/v1/cli-demo",
            TTSBUDDY_CONFIG_DIR="/ambient/config",
        ), profile_scope("routed", {}):
            output = self._synthesize()

        self.assertTrue(output.endswith("speech.mp3"))
        call = self._calls()[0]
        argv = call["argv"]
        self.assertEqual(argv[argv.index("--config-dir") + 1], str(selected_config))
        self.assertNotIn("TTSBUDDY_API_KEY", call["env"])
        self.assertNotIn("TTSBUDDY_API_URL", call["env"])
        self.assertNotIn("TTSBUDDY_CONFIG_DIR", call["env"])

    def test_readiness_uses_local_doctor_and_voice_catalog_is_offline(self):
        with self._call_env(FAKE_MODE="success"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
            self.assertTrue(self.provider.is_available())
            voices = self.provider.list_voices()
        self.assertEqual(voices[0], {"id": "af_heart", "display": "Madison", "language": "American English", "gender": "Female"})
        calls = self._calls()
        self.assertEqual(["doctor" in call["argv"] for call in calls], [True, False])
        voice_argv = calls[1]["argv"]
        self.assertIn("voices", voice_argv)
        self.assertNotIn("--all", voice_argv)
        self.assertNotIn("TTSBUDDY_API_KEY", calls[1]["env"])

    def test_supertonic_language_setting_is_used_but_kokoro_uses_native_language(self):
        self.ctx.settings["language"] = "fr"
        with self._call_env(FAKE_MODE="success"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
            self._synthesize(path=self.root / "supertonic.ogg", voice="st_f1")
            self._synthesize(path=self.root / "kokoro.wav", voice="af_heart")
        calls = self._calls()
        supertonic_args, kokoro_args = (call["argv"] for call in calls)
        self.assertEqual(supertonic_args[supertonic_args.index("--language") + 1], "fr")
        self.assertNotIn("--language", kokoro_args)

    def test_invalid_language_setting_is_ignored_for_kokoro_only(self):
        self.ctx.settings["language"] = "not a language code"
        with self._call_env(FAKE_MODE="success"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
            self._synthesize(path=self.root / "kokoro.mp3", voice="af_heart")
            with self.assertRaises(TTSBuddyError) as caught:
                self._synthesize(path=self.root / "supertonic.mp3", voice="st_f1")
        self.assertEqual(caught.exception.code, "INVALID_LANGUAGE")
        calls = self._calls()
        self.assertEqual(len(calls), 1)
        self.assertNotIn("--language", calls[0]["argv"])

    def test_auth_and_quota_errors_are_sanitized(self):
        for mode, expected_code in (("auth", "AUTH_REQUIRED"), ("quota", "USAGE_LIMIT_EXCEEDED")):
            with self.subTest(mode=mode), self._call_env(FAKE_MODE=mode), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
                with self.assertRaises(TTSBuddyError) as caught:
                    self._synthesize(path=self.root / f"{mode}.mp3")
                self.assertEqual(caught.exception.code, expected_code)
                self.assertNotIn("SECRET-DO-NOT-RETURN", str(caught.exception))
                self.assertNotIn("signed.example.invalid", str(caught.exception))
                self.assertNotIn("ttsb_alpha", str(caught.exception))
                self.assertNotIn("outcome may be uncertain", str(caught.exception))
        self.assertEqual(sum("speak" in call["argv"] for call in self._calls()), 2)

    def test_exact_id_recovery_guidance_omits_raw_streams_and_urls(self):
        with self._call_env(FAKE_MODE="recovery"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
            with self.assertRaises(TTSBuddyError) as caught:
                self._synthesize()
        message = str(caught.exception)
        self.assertEqual(caught.exception.job_id, "job_abc123")
        self.assertIn("ttsbuddy_recover", message)
        self.assertIn("job_abc123", message)
        self.assertNotIn("ttsbuddy status", message)
        self.assertNotIn("ttsbuddy download", message)
        self.assertNotIn("SECRET-DO-NOT-RETURN", message)
        self.assertNotIn("signed.example.invalid", message)

    def test_recover_tool_downloads_only_the_exact_job_with_profile_secret(self):
        requested_job = "job_known123"
        with self._call_env(FAKE_MODE="success"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
            result = json.loads(self.provider.recover_tool({
                "job_id": requested_job,
                "output_path": str(self.root / "recovered.wav"),
            }))

        self.assertTrue(result["success"])
        self.assertEqual(result["job_id"], requested_job)
        self.assertEqual(result["format"], "mp3")
        self.assertTrue(result["path"].endswith("recovered.mp3"))
        self.assertEqual(Path(result["path"]).read_bytes(), b"ID3-test-audio")
        calls = self._calls()
        self.assertEqual(len(calls), 1)
        argv = calls[0]["argv"]
        self.assertEqual(argv[argv.index("download") + 1], requested_job)
        self.assertNotIn("speak", argv)
        self.assertEqual(calls[0]["env"]["TTSBUDDY_API_KEY"], "ttsb_alpha")

    def test_recovery_uses_selected_cli_config_and_never_inherits_ambient_route(self):
        selected_config = self.root / "selected-recovery-config"
        selected_config.mkdir(mode=0o700)
        self.ctx.settings["cli_config_dir"] = str(selected_config)
        with self._call_env(
            FAKE_MODE="success",
            TTSBUDDY_API_KEY="ambient-key-must-not-leak",
            TTSBUDDY_API_URL="https://demo.example.invalid/v1/cli-demo",
            TTSBUDDY_CONFIG_DIR="/ambient/config",
        ), profile_scope("routed", {}):
            self.provider.recover_job("job_config123", str(self.root / "config-route.mp3"))

        call = self._calls()[0]
        argv = call["argv"]
        self.assertEqual(argv[argv.index("--config-dir") + 1], str(selected_config))
        self.assertEqual(argv[argv.index("--execution-context") + 1], "agent")
        self.assertEqual(argv[argv.index("download") + 1], "job_config123")
        self.assertNotIn("TTSBUDDY_API_KEY", call["env"])
        self.assertNotIn("TTSBUDDY_API_URL", call["env"])
        self.assertNotIn("TTSBUDDY_CONFIG_DIR", call["env"])

    def test_recovery_errors_keep_the_requested_job_id_and_safe_guidance(self):
        with self._call_env(FAKE_MODE="download_error"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
            result = json.loads(self.provider.recover_tool({
                "job_id": "job_requested123",
                "output_path": str(self.root / "failed-recovery.mp3"),
            }))
        message = result["error"]["message"]
        self.assertFalse(result["success"])
        self.assertIn("job_requested123", message)
        self.assertIn("ttsbuddy_recover", message)
        self.assertNotIn("job_other123", message)
        self.assertNotIn("SECRET-DO-NOT-RETURN", message)
        self.assertNotIn("signed.example.invalid", message)

    def test_recovery_rejects_unsafe_and_existing_output_paths(self):
        before = self._calls()
        with self.assertRaises(TTSBuddyError) as traversal:
            self.provider.recover_job("job_known123", str(self.root / ".." / "unsafe.wav"))
        self.assertEqual(traversal.exception.code, "INVALID_OUTPUT_PATH")
        self.assertEqual(self._calls(), before)

        target = self.root / "protected.mp3"
        target.write_bytes(b"keep this file")
        with self.assertRaises(TTSBuddyError) as exists:
            self.provider.recover_job("job_known123", str(target))
        self.assertEqual(exists.exception.code, "OUTPUT_EXISTS")
        self.assertEqual(target.read_bytes(), b"keep this file")
        self.assertEqual(self._calls(), before)

    def test_invalid_json_and_bad_download_metadata_leave_no_output(self):
        cases = (
            ("invalid_json", "CLI_FAILED"),
            ("missing_file", "DOWNLOAD_FILE_MISSING"),
            ("wrong_path", "DOWNLOAD_PATH_MISMATCH"),
            ("wrong_bytes", "DOWNLOAD_METADATA_MISMATCH"),
            ("missing_metadata", "DOWNLOAD_METADATA_INVALID"),
            ("wrong_format", "AUDIO_FORMAT_MISMATCH"),
            ("invalid_success_json", "INVALID_CLI_JSON"),
        )
        for mode, expected_code in cases:
            with self.subTest(mode=mode), self._call_env(FAKE_MODE=mode), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
                output = self.root / f"{mode}.ogg"
                before = set(self.root.iterdir())
                with self.assertRaises(TTSBuddyError) as caught:
                    self._synthesize(path=output)
                self.assertEqual(caught.exception.code, expected_code)
                if mode not in ("invalid_json", "invalid_success_json"):
                    self.assertIn("job_success123", str(caught.exception))
                    self.assertIn("ttsbuddy_recover", str(caught.exception))
                self.assertFalse((self.root / f"{mode}.mp3").exists())
                self.assertEqual(set(self.root.iterdir()), before)

    def test_malformed_cli_json_preserves_the_generated_idempotency_key(self):
        for mode in ("invalid_json", "invalid_success_json"):
            with self.subTest(mode=mode), self._call_env(FAKE_MODE=mode), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
                with self.assertRaises(TTSBuddyError) as caught:
                    self._synthesize(path=self.root / f"{mode}.ogg")
            key = caught.exception.idempotency_key
            self.assertRegex(key, provider_module._SAFE_IDEMPOTENCY_KEY)
            self.assertIn(key, str(caught.exception))
            self.assertIn("outcome may be uncertain", str(caught.exception))

    def test_existing_output_is_never_overwritten_and_missing_credentials_fail_closed(self):
        target = self.root / "protected.mp3"
        target.write_bytes(b"keep this file")
        with self._call_env(FAKE_MODE="success"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
            with self.assertRaises(TTSBuddyError) as caught:
                self._synthesize(path=target)
        self.assertEqual(caught.exception.code, "OUTPUT_EXISTS")
        self.assertEqual(target.read_bytes(), b"keep this file")
        self.assertEqual(self._calls(), [])

        with self._call_env(FAKE_MODE="success"), profile_scope("empty", {}):
            self.assertFalse(self.provider.is_available())

    def test_atomic_publish_refuses_a_competing_file_and_preserves_its_bytes(self):
        target = self.root / "raced-output.mp3"

        def competing_writer(_source, destination, **_kwargs):
            Path(destination).write_bytes(b"concurrent writer's file")
            raise FileExistsError

        with (
            self._call_env(FAKE_MODE="success"),
            profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}),
            mock.patch.object(provider_module.os, "link", side_effect=competing_writer),
        ):
            with self.assertRaises(TTSBuddyError) as caught:
                self._synthesize(path=target)

        self.assertEqual(caught.exception.code, "OUTPUT_EXISTS")
        self.assertEqual(caught.exception.job_id, "job_success123")
        self.assertEqual(target.read_bytes(), b"concurrent writer's file")
        self.assertIn("ttsbuddy_recover", str(caught.exception))
        self.assertEqual(len(self._calls()), 1)
        self.assertEqual(list(self.root.glob(".ttsbuddy-hermes-*")), [])

    def test_synthesis_interrupt_cleans_staging_and_keeps_idempotency_key(self):
        def interrupt_after_partial(argv, **_kwargs):
            Path(argv[argv.index("--output") + 1]).write_bytes(b"partial")
            raise KeyboardInterrupt

        with (
            self._call_env(FAKE_MODE="success"),
            profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}),
            mock.patch.object(TTSBuddyProvider, "_run_cli", side_effect=interrupt_after_partial),
        ):
            with self.assertRaises(TTSBuddyError) as caught:
                self._synthesize(path=self.root / "interrupted.mp3")

        self.assertEqual(caught.exception.code, "CLI_INTERRUPTED")
        self.assertRegex(caught.exception.idempotency_key, provider_module._SAFE_IDEMPOTENCY_KEY)
        self.assertIn(caught.exception.idempotency_key, str(caught.exception))
        self.assertFalse((self.root / "interrupted.mp3").exists())
        self.assertEqual(list(self.root.glob(".ttsbuddy-hermes-*")), [])

    def test_invalid_speed_and_poll_timeout_fail_before_starting_cli(self):
        cases = (
            ({"speed": 1.6}, "INVALID_SPEED"),
            ({"settings": {"poll_timeout_seconds": 29}}, "INVALID_TIMEOUT"),
        )
        for index, (request, expected_code) in enumerate(cases):
            with self.subTest(code=expected_code), self._call_env(FAKE_MODE="success"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
                settings = request.pop("settings", None)
                if settings:
                    self.ctx.settings.update(settings)
                with self.assertRaises(TTSBuddyError) as caught:
                    self._synthesize(path=self.root / f"invalid-{index}.mp3", **request)
            self.assertEqual(caught.exception.code, expected_code)
            self.assertEqual(self._calls(), [])

    def test_timeout_kills_local_cli_and_cleans_partial_file(self):
        self.provider._poll_timeout_seconds = lambda: 0.1
        with mock.patch.object(provider_module, "_SUBMISSION_AND_DOWNLOAD_GRACE", 0.2):
            with self._call_env(FAKE_MODE="slow"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
                with self.assertRaises(TTSBuddyError) as caught:
                    self._synthesize(path=self.root / "slow.ogg")
        self.assertEqual(caught.exception.code, "CLI_TIMEOUT")
        self.assertIn("hosted job may still be running", str(caught.exception))
        self.assertFalse((self.root / "slow.mp3").exists())
        self.assertEqual(list(self.root.glob(".ttsbuddy-hermes-*")), [])

    def test_interrupt_terminates_child_before_propagating(self):
        class FakeProcess:
            pid = 4321
            returncode = -9

            def __init__(self):
                self.calls = 0

            def communicate(self, **kwargs):
                self.calls += 1
                if self.calls == 1:
                    raise KeyboardInterrupt
                return "", ""

        process = FakeProcess()
        with mock.patch.object(provider_module.subprocess, "Popen", return_value=process):
            with mock.patch.object(provider_module.os, "killpg") as kill_group:
                with self.assertRaises(KeyboardInterrupt):
                    TTSBuddyProvider._run_cli(["fake"], env={}, timeout=1)
        kill_group.assert_called_once_with(4321, provider_module.signal.SIGKILL)
        self.assertEqual(process.calls, 2)


if __name__ == "__main__":
    unittest.main()
