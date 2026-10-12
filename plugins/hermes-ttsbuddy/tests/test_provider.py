from __future__ import annotations

import argparse
import contextlib
import io
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
cli_module = importlib.import_module("hermes_ttsbuddy.cli")


_ACTIVE_PROFILE: ContextVar[str] = ContextVar("ttsbuddy_test_profile", default="default")


class FakeContext:
    def __init__(self, settings: dict | None = None, profiles: dict | None = None) -> None:
        self.settings = settings or {}
        self.profiles = profiles or {}
        self.registered = []
        self.tools = []
        self.cli_commands = []
        self.commands = {}

    def get_config(self, key, default=None):
        selected = self.profiles.get(_ACTIVE_PROFILE.get(), self.settings)
        return selected.get(key, default)

    def register_tts_provider(self, provider):
        self.registered.append(provider)

    def register_tool(self, **tool):
        self.tools.append(tool)

    def register_cli_command(self, **command):
        self.cli_commands.append(command)

    def register_command(self, name, handler, **metadata):
        self.commands[name] = {"handler": handler, **metadata}


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
read_stdin = "speak" in argv or "--code-stdin" in argv
record = os.environ.get("FAKE_RECORD")
if record:
    with open(record, "a", encoding="utf-8") as stream:
        stream.write(json.dumps({"argv": argv, "env": dict(os.environ), "stdin": sys.stdin.read() if read_stdin else ""}) + "\n")
elif read_stdin:
    sys.stdin.read()

mode = os.environ.get("FAKE_MODE", "success")

if "version" in argv:
    print(json.dumps({"version": os.environ.get("FAKE_VERSION", "0.16.0")}))
    sys.exit(0)

if "account" in argv and "delete" in argv:
    stage = argv[argv.index("delete") + 1]
    if mode == "deletion_timeout":
        time.sleep(5)
    if mode == "deletion_invalid_json":
        print("{not-json")
        sys.exit(0)
    state = os.environ.get("FAKE_DELETION_STATE")
    if state is None:
        state = {"start": "verification_required", "confirm": "completed", "status": "completed", "cancel": "cancelled"}[stage]
    complete = state == "completed"
    successful = state in ("verification_required", "ready", "unknown", "requested", "running",
                           "failed", "none", "completed", "cancelled")
    if "FAKE_DELETION_SUCCESS" in os.environ:
        successful = os.environ["FAKE_DELETION_SUCCESS"] == "true"
    payload = {
        "success": successful,
        "deleted": complete,
        "state": state,
        "challenge_id": os.environ.get("FAKE_DELETION_CHALLENGE_ID", "a" * 32),
        "source": "server_response",
        "request_id": "11111111-2222-3333-4444-555555555555",
        "correlation_id": "66666666-7777-8888-9999-aaaaaaaaaaaa",
        "apple_authorization": "revoked" if complete else "not_linked",
        "verified_at": "2026-10-12T12:00:00Z" if complete else None,
        "expires_at": "2026-10-12T12:30:00Z" if stage == "start" else None,
        "expiry_scope": "cli_continuation_deadline" if stage == "start" else None,
        "local_credentials": "preserved; ownership is not recorded" if complete else None,
        "partial_cleanup": state == "partial",
        "local_receipt_saved": os.environ.get("FAKE_DELETION_LOCAL_RECEIPT_SAVED", "true") == "true",
        "pending_count": 2 if state == "partial" else 0,
        "error_code": os.environ.get("FAKE_DELETION_ERROR_CODE", "DELETION_INCOMPLETE" if not successful else ""),
        "message": "OTP-DO-NOT-RETURN https://secret.example.invalid/token",
        "next_action": "RAW-UNSAFE-NEXT-ACTION",
        "verification_code": "123456",
    }
    if os.environ.get("FAKE_DELETION_OMIT_CHALLENGE") == "true":
        del payload["challenge_id"]
    print(json.dumps(payload))
    if mode == "deletion_timeout_after_receipt":
        sys.stdout.flush()
        time.sleep(5)
    default_exit = "0" if successful else "1"
    sys.exit(int(os.environ.get("FAKE_DELETION_EXIT", default_exit)))

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
        self.assertEqual(len(context.cli_commands), 1)
        self.assertEqual(context.cli_commands[0]["name"], "ttsbuddy")
        self.assertTrue(callable(context.cli_commands[0]["setup_fn"]))
        self.assertTrue(callable(context.cli_commands[0]["handler_fn"]))
        self.assertEqual(set(context.commands), {"ttsbuddy-account"})
        self.assertNotIn("123456", str(context.tools[0]["schema"]))

    def _registered_context(self, settings=None, profiles=None):
        context = FakeContext(self.settings if settings is None else settings, profiles)
        register(context)
        return context

    def _dispatch_registered_cli(self, context, argv):
        from hermes_cli.main import _attach_plugin_cli_command

        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")
        _attach_plugin_cli_command(subparsers, context.cli_commands[0])
        output = io.StringIO()
        errors = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            try:
                args = parser.parse_args(["ttsbuddy", *argv])
                status = args.func(args)
            except SystemExit as exc:
                status = int(exc.code or 0)
        return status, output.getvalue(), errors.getvalue()

    def test_account_deletion_all_stages_use_real_hermes_dispatch_and_cli_profile(self):
        config_dir = self.root / "deletion-cli-config"
        config_dir.mkdir(mode=0o700)
        context = self._registered_context({"cli_path": str(self.cli), "cli_config_dir": str(config_dir)})
        challenge = "a" * 32
        with self._call_env(
            FAKE_MODE="success",
            TTSBUDDY_API_KEY="ambient-speech-key",
            TTSBUDDY_ACCOUNT_DELETION_URL="https://untrusted.invalid/delete",
            TTSBUDDY_CUSTOM_ENDPOINT="https://untrusted.invalid",
        ), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_profile_alpha"}):
            start_rc, start_out, start_err = self._dispatch_registered_cli(
                context, ["account", "delete", "start", "--email", "owner@example.com"],
            )
            with mock.patch.object(cli_module, "_is_tty", return_value=True), \
                    mock.patch.object(cli_module.getpass, "getpass", return_value="123456"):
                confirm_rc, confirm_out, confirm_err = self._dispatch_registered_cli(
                    context, ["account", "delete", "confirm", "--challenge-id", challenge,
                              "--confirm-email", "owner@example.com", "--yes"],
                )
            status_rc, status_out, status_err = self._dispatch_registered_cli(
                context, ["account", "delete", "status", "--challenge-id", challenge],
            )
            cancel_rc, cancel_out, cancel_err = self._dispatch_registered_cli(
                context, ["account", "delete", "cancel", "--challenge-id", challenge],
            )

        self.assertEqual([start_rc, confirm_rc, status_rc, cancel_rc], [0, 0, 0, 0])
        self.assertEqual([start_err, confirm_err, status_err, cancel_err], ["", "", "", ""])
        start_result, confirm_result, status_result, cancel_result = map(
            json.loads, (start_out, confirm_out, status_out, cancel_out),
        )
        self.assertEqual(start_result["state"], "verification_required")
        self.assertEqual(start_result["challenge_id"], challenge)
        self.assertIn(f"confirm --challenge-id {challenge}", start_result["next_action"])
        self.assertEqual(confirm_result["state"], "completed")
        self.assertTrue(confirm_result["deleted"])
        self.assertEqual(confirm_result["request_id"], "11111111-2222-3333-4444-555555555555")
        self.assertEqual(status_result["state"], "completed")
        self.assertEqual(cancel_result["state"], "cancelled")
        self.assertNotIn("owner@example.com", start_out)
        self.assertNotIn("123456", confirm_out)

        calls = self._calls()
        deletion_calls = [call for call in calls if "account" in call["argv"]]
        self.assertEqual(len(calls), 8, "each stage gets one read-only version probe and one CLI delegation")
        self.assertEqual([call["argv"][call["argv"].index("delete") + 1] for call in deletion_calls],
                         ["start", "confirm", "status", "cancel"])
        for call in calls:
            self.assertFalse(any(key.upper().startswith("TTSBUDDY_") for key in call["env"]))
        for call in deletion_calls:
            argv = call["argv"]
            self.assertEqual(argv[argv.index("--config-dir") + 1], str(config_dir.resolve()))
            self.assertIn("--json", argv)
            self.assertNotIn("123456", argv)
        self.assertIn("--code-stdin", deletion_calls[1]["argv"])
        self.assertEqual(deletion_calls[1]["stdin"], "123456\n")
        self.assertNotIn("--code-stdin", deletion_calls[2]["argv"])
        self.assertEqual(deletion_calls[2]["stdin"], "")

    def test_account_deletion_protected_stdin_is_bounded_and_optional_for_status(self):
        config_dir = self.root / "deletion-cli-config"
        config_dir.mkdir(mode=0o700)
        context = self._registered_context({"cli_path": str(self.cli), "cli_config_dir": str(config_dir)})
        challenge = "a" * 32
        with self._call_env(FAKE_MODE="success"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
            with mock.patch.object(cli_module.sys, "stdin", io.StringIO("654321\n")):
                confirm_rc, confirm_out, _ = self._dispatch_registered_cli(
                    context, ["account", "delete", "confirm", "--challenge-id", challenge,
                              "--confirm-email", "owner@example.com", "--yes", "--code-stdin"],
                )
            with mock.patch.object(cli_module.sys, "stdin", io.StringIO("")):
                status_rc, status_out, _ = self._dispatch_registered_cli(
                    context, ["account", "delete", "status", "--challenge-id", challenge, "--code-stdin"],
                )
            with mock.patch.object(cli_module.sys, "stdin", io.StringIO("123456789")):
                too_long_rc, too_long_out, too_long_err = self._dispatch_registered_cli(
                    context, ["account", "delete", "status", "--challenge-id", challenge, "--code-stdin"],
                )

        self.assertEqual([confirm_rc, status_rc, too_long_rc], [0, 0, 2])
        self.assertEqual(too_long_err, "")
        self.assertEqual(json.loads(too_long_out)["error_code"], "INVALID_CODE_INPUT")
        self.assertNotIn("654321", confirm_out)
        self.assertEqual(json.loads(status_out)["state"], "completed")
        actions = [call for call in self._calls() if "account" in call["argv"]]
        self.assertEqual(len(actions), 2, "invalid bounded input must stop before the CLI operation")
        self.assertEqual(actions[0]["stdin"], "654321\n")
        self.assertIn("--code-stdin", actions[1]["argv"])
        self.assertEqual(actions[1]["stdin"], "")

    def test_account_deletion_hidden_input_fails_closed_without_tty_or_echo(self):
        config_dir = self.root / "deletion-cli-config"
        config_dir.mkdir(mode=0o700)
        context = self._registered_context({"cli_path": str(self.cli), "cli_config_dir": str(config_dir)})
        args = ["account", "delete", "confirm", "--challenge-id", "a" * 32,
                "--confirm-email", "owner@example.com", "--yes"]
        with self._call_env(FAKE_MODE="success"), profile_scope("alpha", {}):
            with mock.patch.object(cli_module, "_is_tty", return_value=False):
                no_tty_rc, no_tty_out, _ = self._dispatch_registered_cli(context, args)
            with mock.patch.object(cli_module, "_is_tty", return_value=True), \
                    mock.patch.object(cli_module.getpass, "getpass", side_effect=cli_module.getpass.GetPassWarning):
                hidden_rc, hidden_out, _ = self._dispatch_registered_cli(context, args)

        self.assertEqual([no_tty_rc, hidden_rc], [2, 2])
        self.assertEqual(json.loads(no_tty_out)["error_code"], "CODE_INPUT_REQUIRED")
        self.assertEqual(json.loads(hidden_out)["error_code"], "HIDDEN_INPUT_UNAVAILABLE")
        self.assertNotIn("owner@example.com", no_tty_out + hidden_out)
        calls = self._calls()
        self.assertEqual(len(calls), 2)
        self.assertTrue(all("version" in call["argv"] for call in calls))

    def test_account_deletion_confirmation_and_unknown_args_fail_closed(self):
        config_dir = self.root / "deletion-cli-config"
        config_dir.mkdir(mode=0o700)
        context = self._registered_context({"cli_path": str(self.cli), "cli_config_dir": str(config_dir)})
        challenge = "a" * 32
        with self._call_env(FAKE_MODE="success"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
            missing_rc, missing_out, _ = self._dispatch_registered_cli(
                context, ["account", "delete", "confirm", "--challenge-id", challenge],
            )
            nested_unknown_rc, nested_unknown_out, nested_unknown_err = self._dispatch_registered_cli(
                context, ["account", "delete", "confirm", "--challenge-id", challenge,
                          "--confirm-email", "owner@example.com", "--yes", "--otp", "OTP-SENTINEL-123456"],
            )
            leading_unknown_rc, leading_unknown_out, leading_unknown_err = self._dispatch_registered_cli(
                context, ["--otp", "OTP-SENTINEL-123456"],
            )

        self.assertEqual(missing_rc, 2)
        self.assertEqual(json.loads(missing_out)["error_code"], "CONFIRMATION_REQUIRED")
        self.assertEqual([nested_unknown_rc, leading_unknown_rc], [2, 2])
        self.assertNotIn("OTP-SENTINEL-123456", nested_unknown_out + nested_unknown_err + leading_unknown_out + leading_unknown_err)
        self.assertIn("Invalid TTS Buddy account command arguments", nested_unknown_err)
        self.assertIn("Invalid TTS Buddy account command arguments", leading_unknown_err)
        self.assertEqual(self._calls(), [])

    def test_account_deletion_chat_command_is_guidance_only(self):
        context = self._registered_context()
        handler = context.commands["ttsbuddy-account"]["handler"]
        guidance = handler("confirm --confirm-email owner@example.com --yes OTP-SENTINEL-123456")
        self.assertIn("local Hermes terminal", guidance)
        self.assertNotIn("owner@example.com", guidance)
        self.assertNotIn("123456", guidance)
        self.assertEqual(self._calls(), [])

    def test_account_deletion_requires_explicit_config_and_supported_cli_only_for_deletion(self):
        no_config = self._registered_context({"cli_path": str(self.cli)})
        with self._call_env(FAKE_MODE="success"), profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_speech_only"}):
            missing_rc, missing_out, _ = self._dispatch_registered_cli(
                no_config, ["account", "delete", "start", "--email", "owner@example.com"],
            )
        self.assertEqual(missing_rc, 2)
        self.assertEqual(json.loads(missing_out)["error_code"], "CLI_CONFIG_DIR_REQUIRED")
        self.assertEqual(self._calls(), [])

        config_dir = self.root / "deletion-cli-config"
        config_dir.mkdir(mode=0o700)
        context = self._registered_context({"cli_path": str(self.cli), "cli_config_dir": str(config_dir)})
        with self._call_env(FAKE_MODE="success", FAKE_VERSION="0.15.1"), \
                profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_speech_only"}):
            old_rc, old_out, _ = self._dispatch_registered_cli(
                context, ["account", "delete", "start", "--email", "owner@example.com"],
            )
        self.assertEqual(old_rc, 2)
        self.assertEqual(json.loads(old_out)["error_code"], "CLI_VERSION_UNSUPPORTED")
        self.assertEqual(len(self._calls()), 1)
        self.assertNotIn("account", self._calls()[0]["argv"])

        self.record.write_text("", encoding="utf-8")
        with self._call_env(FAKE_MODE="success", FAKE_VERSION="0.15.1"), \
                profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_speech_only"}):
            self._synthesize(path=self.root / "old-cli-speech.mp3")
        speech_calls = self._calls()
        self.assertEqual(len(speech_calls), 1)
        self.assertIn("speak", speech_calls[0]["argv"])
        self.assertNotIn("version", speech_calls[0]["argv"])

    def test_account_deletion_profile_settings_refresh_for_each_stage(self):
        first_config = self.root / "profile-a-config"
        second_config = self.root / "profile-b-config"
        first_config.mkdir(mode=0o700)
        second_config.mkdir(mode=0o700)
        context = self._registered_context(profiles={
            "alpha": {"cli_path": str(self.cli), "cli_config_dir": str(first_config)},
            "beta": {"cli_path": str(self.cli), "cli_config_dir": str(second_config)},
        })
        with self._call_env(FAKE_MODE="success"):
            with profile_scope("alpha", {}):
                self._dispatch_registered_cli(context, ["account", "delete", "start", "--email", "owner@example.com"])
            with profile_scope("beta", {}):
                self._dispatch_registered_cli(context, ["account", "delete", "status", "--challenge-id", "a" * 32])
        deletion_calls = [call for call in self._calls() if "account" in call["argv"]]
        self.assertEqual([call["argv"][call["argv"].index("--config-dir") + 1] for call in deletion_calls],
                         [str(first_config.resolve()), str(second_config.resolve())])

    def test_account_deletion_preserves_unknown_partial_mismatch_and_timeout_states(self):
        config_dir = self.root / "deletion-cli-config"
        config_dir.mkdir(mode=0o700)
        context = self._registered_context({"cli_path": str(self.cli), "cli_config_dir": str(config_dir)})
        challenge = "a" * 32

        with self._call_env(FAKE_MODE="success", FAKE_DELETION_STATE="unknown", FAKE_DELETION_SUCCESS="true"), \
                profile_scope("alpha", {}):
            malformed_rc, malformed_out, _ = self._dispatch_registered_cli(
                context, ["account", "delete", "status", "--challenge-id", challenge],
            )
        self.assertEqual(malformed_rc, 1)
        malformed = json.loads(malformed_out)
        self.assertEqual(malformed["state"], "unknown")
        self.assertFalse(malformed["success"])
        self.assertNotIn("OTP-DO-NOT-RETURN", malformed_out)
        self.assertNotIn("RAW-UNSAFE-NEXT-ACTION", malformed_out)

        with self._call_env(FAKE_MODE="success", FAKE_DELETION_STATE="partial"), \
                profile_scope("alpha", {}):
            partial_rc, partial_out, _ = self._dispatch_registered_cli(
                context, ["account", "delete", "status", "--challenge-id", challenge],
            )
        self.assertEqual(partial_rc, 1)
        partial = json.loads(partial_out)
        self.assertEqual(partial["state"], "partial")
        self.assertTrue(partial["partial_cleanup"])
        self.assertEqual(partial["request_id"], "11111111-2222-3333-4444-555555555555")

        for continuation_state in ("verification_required", "ready", "unknown", "requested",
                                   "running", "failed", "none"):
            with self.subTest(start_state=continuation_state), \
                    self._call_env(FAKE_MODE="success", FAKE_DELETION_STATE=continuation_state,
                                   FAKE_DELETION_SUCCESS="true"), profile_scope("alpha", {}):
                start_rc, start_out, _ = self._dispatch_registered_cli(
                    context, ["account", "delete", "start", "--email", "owner@example.com"],
                )
            self.assertEqual(start_rc, 0)
            self.assertEqual(json.loads(start_out)["state"], continuation_state)
            self.assertFalse(json.loads(start_out)["deleted"])

        with self._call_env(FAKE_MODE="success", FAKE_DELETION_CHALLENGE_ID="b" * 32), \
                profile_scope("alpha", {}):
            mismatch_rc, mismatch_out, _ = self._dispatch_registered_cli(
                context, ["account", "delete", "status", "--challenge-id", challenge],
            )
        self.assertEqual(mismatch_rc, 1)
        self.assertEqual(json.loads(mismatch_out)["error_code"], "CLI_RESPONSE_MISMATCH")

        with self._call_env(FAKE_MODE="success", FAKE_DELETION_STATE="completed",
                            FAKE_DELETION_OMIT_CHALLENGE="true"), profile_scope("alpha", {}):
            missing_id_rc, missing_id_out, _ = self._dispatch_registered_cli(
                context, ["account", "delete", "status", "--challenge-id", challenge],
            )
        self.assertEqual(missing_id_rc, 1)
        missing_id = json.loads(missing_id_out)
        self.assertEqual(missing_id["error_code"], "CLI_RESPONSE_INVALID")
        self.assertFalse(missing_id["success"])
        self.assertFalse(missing_id["deleted"])
        self.assertEqual(missing_id["state"], "unknown")
        self.assertIn("Inspect challenge", missing_id["next_action"])
        self.assertEqual(missing_id["challenge_id"], challenge)

        with self._call_env(FAKE_MODE="success", FAKE_DELETION_STATE="completed",
                            FAKE_DELETION_LOCAL_RECEIPT_SAVED="false",
                            FAKE_DELETION_ERROR_CODE="LOCAL_RECEIPT_SAVE_FAILED", FAKE_DELETION_EXIT="1"), \
                profile_scope("alpha", {}), \
                mock.patch.object(cli_module.sys, "stdin", io.StringIO("")):
            receipt_rc, receipt_out, _ = self._dispatch_registered_cli(
                context, ["account", "delete", "confirm", "--challenge-id", challenge,
                          "--confirm-email", "owner@example.com", "--yes", "--code-stdin"],
            )
        self.assertEqual(receipt_rc, 1)
        completed_receipt = json.loads(receipt_out)
        self.assertEqual(completed_receipt["state"], "completed")
        self.assertTrue(completed_receipt["deleted"])
        self.assertFalse(completed_receipt["local_receipt_saved"])
        self.assertIn("Retain this verified completion receipt", completed_receipt["cleanup_warning"])
        self.assertIn("remove temporary authentication state", completed_receipt["cleanup_warning"])
        self.assertNotIn("retain the receipt and remove temporary authentication state", receipt_out)

        self.record.write_text("", encoding="utf-8")
        with self._call_env(FAKE_MODE="deletion_timeout"), \
                profile_scope("alpha", {}), \
                mock.patch.object(cli_module, "_DELETE_TIMEOUT_SECONDS", 0.05):
            timeout_rc, timeout_out, _ = self._dispatch_registered_cli(
                context, ["account", "delete", "status", "--challenge-id", challenge],
            )
        self.assertEqual(timeout_rc, 1)
        self.assertEqual(json.loads(timeout_out)["error_code"], "CLI_TIMEOUT")
        self.assertEqual(json.loads(timeout_out)["state"], "unknown")
        self.assertFalse(json.loads(timeout_out)["deleted"])
        self.assertEqual(sum("account" in call["argv"] for call in self._calls()), 1)

        self.record.write_text("", encoding="utf-8")
        with self._call_env(FAKE_MODE="deletion_timeout_after_receipt", FAKE_DELETION_STATE="completed",
                            FAKE_DELETION_LOCAL_RECEIPT_SAVED="false"), \
                profile_scope("alpha", {}), mock.patch.object(cli_module, "_DELETE_TIMEOUT_SECONDS", 0.05):
            completed_timeout_rc, completed_timeout_out, _ = self._dispatch_registered_cli(
                context, ["account", "delete", "status", "--challenge-id", challenge],
            )
        self.assertEqual(completed_timeout_rc, 1)
        completed_timeout = json.loads(completed_timeout_out)
        self.assertEqual(completed_timeout["error_code"], "CLI_TIMEOUT")
        self.assertEqual(completed_timeout["state"], "completed")
        self.assertTrue(completed_timeout["deleted"])
        self.assertFalse(completed_timeout["local_receipt_saved"])
        self.assertIn("remove temporary authentication state", completed_timeout["cleanup_warning"])

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

    def test_hermes_failed_dispatch_preserves_preexisting_output(self):
        from tools import tts_tool
        import tools.tts_tool_plugins as tts_tool_plugins

        target = self.root / "existing.mp3"
        original = b"preexisting user file"
        target.write_bytes(original)
        with mock.patch.object(tts_tool, "_load_tts_config", return_value={"provider": "ttsbuddy", "voice": "st_m1"}), \
                mock.patch.object(tts_tool_plugins, "_lookup_plugin_provider", return_value=self.provider), \
                mock.patch.object(self.provider, "synthesize", wraps=self.provider.synthesize) as synthesize, \
                self._call_env(FAKE_MODE="quota"), \
                profile_scope("alpha", {"TTSBUDDY_API_KEY": "ttsb_alpha"}):
            with self.assertLogs(tts_tool.logger, level="ERROR"):
                result = json.loads(tts_tool.text_to_speech_tool("hello", output_path=str(target)))

        self.assertFalse(result["success"])
        self.assertEqual(target.read_bytes(), original)
        plugin_output = Path(synthesize.call_args.args[1])
        self.assertNotEqual(plugin_output, target)
        self.assertTrue(plugin_output.name.startswith(".existing."))
        self.assertFalse(plugin_output.exists())
        calls = self._calls()
        self.assertEqual(sum("speak" in call["argv"] for call in calls), 1)
        unexpected = {path.name for path in self.root.iterdir()} - {
            "calls.jsonl", "hermes-home", self.cli.name, "existing.mp3",
        }
        self.assertEqual(unexpected, set())

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
