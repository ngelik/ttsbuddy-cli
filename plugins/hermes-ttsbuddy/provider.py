"""Hermes TTS provider that delegates all API work to the TTS Buddy CLI."""

from __future__ import annotations

import json
import math
import os
import re
import shutil
import signal
import stat
import subprocess
import tempfile
import uuid
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterator, Mapping, Optional

from agent.tts_provider import TTSProvider


_PLUGIN_ID = "ttsbuddy"
_SECRET_ENV = "TTSBUDDY_API_KEY"
_DEFAULT_CLI = "ttsbuddy"
_DEFAULT_POLL_TIMEOUT = 600
_MIN_POLL_TIMEOUT = 30
_MAX_POLL_TIMEOUT = 1800
_SUBMISSION_AND_DOWNLOAD_GRACE = 420  # CLI download is bounded at five minutes.
_MAX_JSON_BYTES = 1024 * 1024
_SAFE_CODE = re.compile(r"^[A-Z][A-Z0-9_]{0,63}$")
_SAFE_JOB_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$")
_SAFE_IDEMPOTENCY_KEY = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
_LANGUAGE_CODE = re.compile(r"^[A-Za-z]{2,8}(?:-[A-Za-z]{2,8})?$")


class TTSBuddyError(RuntimeError):
    """Sanitized provider failure safe to return through Hermes."""

    def __init__(
        self,
        message: str,
        *,
        code: str = "TTSBUDDY_ERROR",
        job_id: Optional[str] = None,
        idempotency_key: Optional[str] = None,
    ) -> None:
        super().__init__(message + _recovery_guidance(job_id=job_id, request_key=idempotency_key))
        self.code = code
        self.job_id = job_id
        self.idempotency_key = idempotency_key


@dataclass(frozen=True)
class _CredentialRoute:
    kind: str
    secret: Optional[str] = None
    config_dir: Optional[str] = None


@dataclass(frozen=True)
class _CommandResult:
    returncode: int
    stdout: str


class _CommandTimeout(Exception):
    def __init__(self, stdout: str = "") -> None:
        super().__init__("command timed out")
        self.stdout = stdout


class TTSBuddyProvider(TTSProvider):
    """A non-streaming provider backed by the installed ``ttsbuddy`` executable."""

    streams_pcm = False
    stream_sample_rate = None

    def __init__(self, ctx: Any) -> None:
        # Keep the Hermes context, not resolved settings or credentials. Hermes can dispatch
        # the same plugin code under multiple profile scopes in one process.
        self._ctx = ctx

    @property
    def name(self) -> str:
        return _PLUGIN_ID

    @property
    def display_name(self) -> str:
        return "TTS Buddy"

    @property
    def voice_compatible(self) -> bool:
        # Hermes's native TTS dispatcher verifies and converts downloaded MP3 to
        # Opus for voice-bubble platforms before delivering the audio.
        return True

    def default_voice(self) -> Optional[str]:
        return "st_m1"

    def get_setup_schema(self) -> dict[str, Any]:
        return {
            "name": self.display_name,
            "badge": "CLI",
            "tag": "Uses the installed TTS Buddy CLI",
            "env_vars": [{
                "key": _SECRET_ENV,
                "prompt": "TTS Buddy agent API key",
                "url": "https://www.ttsbuddy.com/auth.md",
            }],
        }

    def is_available(self) -> bool:
        """Check executable and local CLI auth readiness without network requests."""
        try:
            executable = self._resolve_executable()
            if not executable:
                return False
            route = self._resolve_credentials()
            if route is None:
                return False
            timeout = self._poll_timeout_seconds()
            with self._cli_config_dir(route) as config_dir:
                result = self._run_cli(
                    [executable, "--json", "--config-dir", config_dir, "doctor"],
                    env=self._child_env(route.secret),
                    timeout=5,
                )
            report = self._parse_json(result.stdout)
            return (
                result.returncode == 0
                and isinstance(report, dict)
                and report.get("ready") is True
                and timeout is not None
            )
        except Exception:
            # Hermes calls availability while painting pickers; never let a local probe raise.
            return False

    def list_voices(self) -> list[dict[str, Any]]:
        """Read the CLI's curated offline voice list; never request ``--all``."""
        try:
            executable = self._resolve_executable()
            if not executable:
                return []
            result = self._run_cli(
                [executable, "--json", "voices"],
                env=self._child_env(None),
                timeout=5,
            )
            if result.returncode != 0:
                return []
            voices = self._parse_json(result.stdout)
            if not isinstance(voices, list):
                return []
            mapped: list[dict[str, Any]] = []
            for voice in voices:
                if not isinstance(voice, dict):
                    continue
                voice_id = voice.get("id")
                if not isinstance(voice_id, str) or not voice_id.strip():
                    continue
                entry: dict[str, Any] = {
                    "id": voice_id,
                    "display": voice.get("name") if isinstance(voice.get("name"), str) and voice["name"] else voice_id,
                }
                for source, target in (("language", "language"), ("gender", "gender")):
                    value = voice.get(source)
                    if isinstance(value, str) and value:
                        entry[target] = value
                mapped.append(entry)
            return mapped
        except Exception:
            return []

    def synthesize(
        self,
        text: str,
        output_path: str,
        *,
        voice: Optional[str] = None,
        model: Optional[str] = None,
        speed: Optional[float] = None,
        format: str = "mp3",
        **extra: Any,
    ) -> str:
        """Synthesize once through the CLI and return the verified local MP3 path."""
        del model, format  # Voice selects the engine; the CLI's downloaded artifact is MP3.
        if not isinstance(text, str) or not text.strip():
            raise TTSBuddyError("TTS Buddy needs non-empty text.", code="INVALID_TEXT")

        request_key = str(uuid.uuid4())
        final_path = self._validated_output_mp3_path(output_path)
        if os.path.lexists(final_path):
            raise TTSBuddyError("The MP3 output path already exists; no file was overwritten.", code="OUTPUT_EXISTS")
        if not final_path.parent.is_dir():
            raise TTSBuddyError("The MP3 output directory is unavailable.", code="OUTPUT_DIRECTORY_UNAVAILABLE")

        try:
            executable = self._resolve_executable()
            if not executable:
                raise TTSBuddyError(
                    "TTS Buddy CLI was not found. Install it or set this Hermes profile's cli_path.",
                    code="CLI_NOT_FOUND",
                )
            route = self._resolve_credentials()
            if route is None:
                raise TTSBuddyError(
                    "No TTS Buddy credential is configured for this Hermes profile. Set its api_key secret or select cli_config_dir.",
                    code="AUTH_REQUIRED",
                )
            poll_seconds = self._poll_timeout_seconds()
            if poll_seconds is None:
                raise TTSBuddyError(
                    "poll_timeout_seconds must be an integer from 30 through 1800.",
                    code="INVALID_TIMEOUT",
                )
            selected_voice = voice.strip() if isinstance(voice, str) and voice.strip() else self.default_voice()
            if not selected_voice:
                raise TTSBuddyError("No TTS Buddy voice is available.", code="VOICE_UNAVAILABLE")
            selected_speed = self._validated_speed(speed)
            language = self._selected_language(extra) if selected_voice.lower().startswith("st_") else ""

            with tempfile.TemporaryDirectory(prefix=".ttsbuddy-hermes-", dir=str(final_path.parent)) as staging_dir:
                staged_audio = os.path.abspath(os.path.join(staging_dir, "audio.mp3"))
                argv = [
                    executable,
                    "--json",
                    "--execution-context", "agent",
                    "speak", "-",
                    "--voice", selected_voice,
                    "--speed", format_speed(selected_speed),
                    "--idempotency-key", request_key,
                    "--output", staged_audio,
                    "--timeout", f"{poll_seconds}s",
                ]
                if language:
                    argv.extend(("--language", language))

                with self._cli_config_dir(route) as config_dir:
                    try:
                        result = self._run_cli(
                            [argv[0], "--json", "--execution-context", "agent", "--config-dir", config_dir, *argv[4:]],
                            env=self._child_env(route.secret),
                            timeout=poll_seconds + _SUBMISSION_AND_DOWNLOAD_GRACE,
                            input_text=text,
                        )
                    except _CommandTimeout as exc:
                        partial = self._parse_json_or_none(exc.stdout)
                        if isinstance(partial, dict) and partial.get("success") is False:
                            raise self._error_from_payload(partial, request_key)
                        raise TTSBuddyError(
                            "The local TTS Buddy CLI timed out and was stopped. The hosted job may still be running.",
                            code="CLI_TIMEOUT",
                            idempotency_key=request_key,
                        ) from None

                payload = self._parse_json_or_none(result.stdout)
                if result.returncode != 0:
                    if isinstance(payload, dict):
                        raise self._error_from_payload(payload, request_key)
                    raise TTSBuddyError(
                        "TTS Buddy CLI failed without a valid JSON error response.",
                        code="CLI_FAILED",
                        idempotency_key=request_key,
                    )
                if not isinstance(payload, dict):
                    raise TTSBuddyError(
                        "TTS Buddy CLI returned invalid JSON; the downloaded file was discarded.",
                        code="INVALID_CLI_JSON",
                        idempotency_key=request_key,
                    )
                self._validate_download(payload, staged_audio, request_key=request_key)
                try:
                    # link() is an atomic no-overwrite publication on the same filesystem.
                    os.link(staged_audio, final_path, follow_symlinks=False)
                except FileExistsError:
                    raise TTSBuddyError(
                        "The MP3 output path appeared during synthesis; no file was overwritten.",
                        code="OUTPUT_EXISTS",
                        job_id=self._payload_job_id(payload),
                        idempotency_key=request_key,
                    ) from None
                except OSError:
                    raise TTSBuddyError(
                        "TTS Buddy finished, but the MP3 could not be placed at the requested path.",
                        code="OUTPUT_FINALIZE_FAILED",
                        job_id=self._payload_job_id(payload),
                        idempotency_key=request_key,
                    ) from None
                return os.path.abspath(final_path)
        except TTSBuddyError:
            raise
        except KeyboardInterrupt:
            raise TTSBuddyError(
                "The local TTS Buddy CLI was interrupted. The hosted job may still be running.",
                code="CLI_INTERRUPTED",
                idempotency_key=request_key,
            ) from None
        except Exception:
            # Never pass raw process, filesystem, JSON, or credential-related errors to Hermes.
            raise TTSBuddyError(
                "TTS Buddy could not complete the request. Check this profile's CLI and credential settings.",
                code="TTSBUDDY_ERROR",
                idempotency_key=request_key,
            ) from None

    def recover_tool(self, args: Any, **_kwargs: Any) -> str:
        """Hermes tool handler for explicit exact-ID recovery."""
        args = args if isinstance(args, dict) else {}
        job_id = args.get("job_id")
        output_path = args.get("output_path")
        try:
            path = self.recover_job(job_id, output_path)
            return json.dumps({"success": True, "provider": self.name, "job_id": job_id, "path": path, "format": "mp3"})
        except TTSBuddyError as exc:
            return json.dumps({
                "success": False,
                "provider": self.name,
                "error": {"code": exc.code, "message": str(exc)},
            })
        except Exception:
            return json.dumps({
                "success": False,
                "provider": self.name,
                "error": {"code": "RECOVERY_FAILED", "message": "TTS Buddy could not recover that job."},
            })

    def recover_job(self, job_id: Any, output_path: Any) -> str:
        """Download a previously returned job by its exact ID; never submit speech."""
        if not isinstance(job_id, str) or not _SAFE_JOB_ID.fullmatch(job_id):
            raise TTSBuddyError("Provide the exact job ID returned by TTS Buddy.", code="JOB_ID_REQUIRED")
        final_path = self._validated_output_mp3_path(output_path)
        if os.path.lexists(final_path):
            raise TTSBuddyError("The MP3 output path already exists; no file was overwritten.", code="OUTPUT_EXISTS")
        if not final_path.parent.is_dir():
            raise TTSBuddyError("The MP3 output directory is unavailable.", code="OUTPUT_DIRECTORY_UNAVAILABLE")

        executable = self._resolve_executable()
        if not executable:
            raise TTSBuddyError(
                "TTS Buddy CLI was not found. Install it or set this Hermes profile's cli_path.",
                code="CLI_NOT_FOUND",
            )
        route = self._resolve_credentials()
        if route is None:
            raise TTSBuddyError(
                "No TTS Buddy credential is configured for this Hermes profile. Set its api_key secret or select cli_config_dir.",
                code="AUTH_REQUIRED",
            )
        poll_seconds = self._poll_timeout_seconds()
        if poll_seconds is None:
            raise TTSBuddyError("poll_timeout_seconds must be an integer from 30 through 1800.", code="INVALID_TIMEOUT")

        with tempfile.TemporaryDirectory(prefix=".ttsbuddy-hermes-recover-", dir=str(final_path.parent)) as staging_dir:
            staged_audio = os.path.abspath(os.path.join(staging_dir, "audio.mp3"))
            try:
                with self._cli_config_dir(route) as config_dir:
                    result = self._run_cli(
                        [
                            executable, "--json", "--execution-context", "agent", "--config-dir", config_dir,
                            "download", job_id, "--output", staged_audio, "--timeout", f"{poll_seconds}s",
                        ],
                        env=self._child_env(route.secret),
                        timeout=poll_seconds + _SUBMISSION_AND_DOWNLOAD_GRACE,
                    )
            except _CommandTimeout as exc:
                partial = self._parse_json_or_none(exc.stdout)
                if isinstance(partial, dict) and partial.get("success") is False:
                    raise self._error_from_payload(partial, None, fallback_job_id=job_id)
                raise TTSBuddyError(
                    "The local TTS Buddy download timed out; the existing hosted job was not cancelled.",
                    code="RECOVERY_TIMEOUT",
                    job_id=job_id,
                ) from None
            except KeyboardInterrupt:
                raise TTSBuddyError(
                    "The local TTS Buddy download was interrupted; the existing hosted job was not cancelled.",
                    code="RECOVERY_INTERRUPTED",
                    job_id=job_id,
                ) from None
            except TTSBuddyError as exc:
                raise TTSBuddyError(str(exc), code=exc.code, job_id=job_id) from None

            payload = self._parse_json_or_none(result.stdout)
            if result.returncode != 0:
                if isinstance(payload, dict):
                    raise self._error_from_payload(payload, None, fallback_job_id=job_id)
                raise TTSBuddyError(
                    "TTS Buddy CLI could not download the existing job.",
                    code="RECOVERY_FAILED",
                    job_id=job_id,
                )
            if not isinstance(payload, dict):
                raise TTSBuddyError(
                    "TTS Buddy CLI returned invalid JSON while downloading the existing job.",
                    code="INVALID_CLI_JSON",
                    job_id=job_id,
                )
            self._validate_download(payload, staged_audio, fallback_job_id=job_id)
            try:
                os.link(staged_audio, final_path, follow_symlinks=False)
            except FileExistsError:
                raise TTSBuddyError(
                    "The MP3 output path appeared during recovery; no file was overwritten.",
                    code="OUTPUT_EXISTS",
                    job_id=job_id,
                ) from None
            except OSError:
                raise TTSBuddyError(
                    "TTS Buddy recovered the job, but the MP3 could not be placed at the requested path.",
                    code="OUTPUT_FINALIZE_FAILED",
                    job_id=job_id,
                ) from None
        return os.path.abspath(final_path)

    def _setting(self, key: str, default: Any = None) -> Any:
        try:
            return self._ctx.get_config(key, default)
        except Exception:
            raise TTSBuddyError(
                "Could not read TTS Buddy settings from the active Hermes profile.",
                code="PROFILE_SETTINGS_UNAVAILABLE",
            ) from None

    def _resolve_executable(self) -> Optional[str]:
        value = self._setting("cli_path", _DEFAULT_CLI)
        candidate = value.strip() if isinstance(value, str) and value.strip() else _DEFAULT_CLI
        if os.path.sep in candidate or (os.path.altsep and os.path.altsep in candidate):
            path = Path(candidate).expanduser()
            if not path.is_absolute() or not path.is_file() or not os.access(path, os.X_OK):
                return None
            return str(path)
        found = shutil.which(candidate)
        return os.path.abspath(found) if found else None

    def _resolve_credentials(self) -> Optional[_CredentialRoute]:
        try:
            from agent.secret_scope import get_secret

            secret = get_secret(_SECRET_ENV)
        except Exception:
            # In particular, do not fall back to os.environ when Hermes reports a missing profile scope.
            raise TTSBuddyError(
                "Could not resolve credentials for the active Hermes profile; check its api_key secret.",
                code="PROFILE_SECRET_UNAVAILABLE",
            ) from None
        if isinstance(secret, str) and secret.strip():
            return _CredentialRoute(kind="secret", secret=secret)

        configured = self._setting("cli_config_dir", "")
        if not isinstance(configured, str) or not configured.strip():
            return None
        path = Path(configured.strip()).expanduser()
        if not path.is_absolute() or not path.is_dir():
            return None
        return _CredentialRoute(kind="config_dir", config_dir=str(path))

    def _poll_timeout_seconds(self) -> Optional[int]:
        value = self._setting("poll_timeout_seconds", _DEFAULT_POLL_TIMEOUT)
        if isinstance(value, bool) or not isinstance(value, int):
            return None
        if value < _MIN_POLL_TIMEOUT or value > _MAX_POLL_TIMEOUT:
            return None
        return value

    def _selected_language(self, extra: Mapping[str, Any]) -> str:
        value = extra.get("language")
        if not isinstance(value, str) or not value.strip():
            value = self._setting("language", "")
        if not isinstance(value, str) or not value.strip():
            return ""
        normalized = value.strip().lower()
        if not _LANGUAGE_CODE.fullmatch(normalized):
            raise TTSBuddyError(
                "language must be a valid language code such as en, fr, or zh-Hant.",
                code="INVALID_LANGUAGE",
            )
        return normalized

    @staticmethod
    def _validated_speed(speed: Optional[float]) -> float:
        value = 1.0 if speed is None else speed
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise TTSBuddyError("TTS Buddy speed must be between 0.5 and 1.5.", code="INVALID_SPEED")
        resolved = float(value)
        if not math.isfinite(resolved) or resolved < 0.5 or resolved > 1.5:
            raise TTSBuddyError("TTS Buddy speed must be between 0.5 and 1.5.", code="INVALID_SPEED")
        return resolved

    @staticmethod
    def _validated_output_mp3_path(output_path: str) -> Path:
        if not isinstance(output_path, str) or not output_path.strip():
            raise TTSBuddyError("Hermes did not provide an output path.", code="OUTPUT_PATH_REQUIRED")
        try:
            from tools.path_security import has_traversal_component, has_unsafe_path_chars
            from agent.file_safety import is_write_approval_required, is_write_denied
        except Exception:
            raise TTSBuddyError("Hermes output path safeguards are unavailable.", code="OUTPUT_PATH_GUARD_UNAVAILABLE") from None
        if has_unsafe_path_chars(output_path) or has_traversal_component(output_path):
            raise TTSBuddyError("The requested output path contains unsafe characters or traversal.", code="INVALID_OUTPUT_PATH")
        path = Path(output_path).expanduser()
        if not path.name:
            raise TTSBuddyError("The requested output path is invalid.", code="INVALID_OUTPUT_PATH")
        path = path.with_suffix(".mp3")
        try:
            if is_write_denied(str(path)) or is_write_approval_required(str(path)):
                raise TTSBuddyError("The requested output path is protected by Hermes.", code="OUTPUT_PATH_PROTECTED")
        except TTSBuddyError:
            raise
        except Exception:
            raise TTSBuddyError("Hermes could not verify the requested output path.", code="OUTPUT_PATH_GUARD_FAILED") from None
        return path

    @contextmanager
    def _cli_config_dir(self, route: _CredentialRoute) -> Iterator[str]:
        if route.kind == "config_dir" and route.config_dir:
            yield route.config_dir
            return
        with tempfile.TemporaryDirectory(prefix="ttsbuddy-hermes-cli-") as directory:
            yield directory

    @staticmethod
    def _child_env(api_key: Optional[str]) -> dict[str, str]:
        # All CLI behavior that can redirect endpoints, select other config, change defaults,
        # or mark the caller as something else is passed explicitly or removed.
        env = {key: value for key, value in os.environ.items() if not key.upper().startswith("TTSBUDDY_")}
        if api_key:
            env[_SECRET_ENV] = api_key
        return env

    @staticmethod
    def _run_cli(
        argv: list[str],
        *,
        env: Mapping[str, str],
        timeout: float,
        input_text: Optional[str] = None,
    ) -> _CommandResult:
        """Run argv directly, bound the child, and never return stderr to the caller."""
        options: dict[str, Any] = {
            "stdin": subprocess.PIPE if input_text is not None else subprocess.DEVNULL,
            "stdout": subprocess.PIPE,
            "stderr": subprocess.PIPE,
            "text": True,
            "env": dict(env),
        }
        if os.name == "posix":
            options["start_new_session"] = True
        elif os.name == "nt":  # pragma: no cover - Windows release support
            options["creationflags"] = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
        try:
            process = subprocess.Popen(argv, **options)
        except OSError:
            raise TTSBuddyError(
                "Could not start TTS Buddy CLI. Check cli_path and executable permissions.",
                code="CLI_START_FAILED",
            ) from None
        try:
            stdout, _stderr = process.communicate(input=input_text, timeout=timeout)
        except subprocess.TimeoutExpired as exc:
            partial = exc.output
            TTSBuddyProvider._stop_process(process)
            stdout, _stderr = process.communicate()
            captured = stdout if stdout else _to_text(partial)
            raise _CommandTimeout(captured) from None
        except BaseException:
            TTSBuddyProvider._stop_process(process)
            process.communicate()
            raise
        if len(stdout.encode("utf-8", errors="replace")) > _MAX_JSON_BYTES:
            raise TTSBuddyError("TTS Buddy CLI returned an oversized JSON response.", code="CLI_RESPONSE_TOO_LARGE")
        return _CommandResult(process.returncode, stdout)

    @staticmethod
    def _stop_process(process: subprocess.Popen[str]) -> None:
        try:
            if os.name == "posix":
                os.killpg(process.pid, signal.SIGKILL)
            else:  # pragma: no cover - Windows release support
                process.kill()
        except (OSError, ProcessLookupError):
            pass

    @staticmethod
    def _parse_json(value: str) -> Any:
        if len(value.encode("utf-8", errors="replace")) > _MAX_JSON_BYTES:
            raise ValueError("response too large")
        return json.loads(value)

    @classmethod
    def _parse_json_or_none(cls, value: str) -> Any:
        try:
            return cls._parse_json(value)
        except (ValueError, TypeError, UnicodeError):
            return None

    def _validate_download(
        self,
        payload: dict[str, Any],
        staged_path: str,
        *,
        request_key: Optional[str] = None,
        fallback_job_id: Optional[str] = None,
    ) -> None:
        job_id = self._payload_job_id(payload) or fallback_job_id
        download = payload.get("download")
        audio = payload.get("audio")
        if not isinstance(download, dict):
            raise TTSBuddyError(
                "TTS Buddy CLI omitted download metadata.",
                code="DOWNLOAD_METADATA_INVALID",
                job_id=job_id,
                idempotency_key=request_key,
            )
        reported_path = download.get("path") if isinstance(download, dict) else None
        byte_count = download.get("bytes") if isinstance(download, dict) else None
        if payload.get("success") is not True or payload.get("status") != "completed":
            raise TTSBuddyError(
                "TTS Buddy CLI did not report a completed synthesis.",
                code="INCOMPLETE_CLI_RESPONSE",
                job_id=job_id,
                idempotency_key=request_key,
            )
        if not isinstance(reported_path, str) or os.path.abspath(reported_path) != os.path.abspath(staged_path):
            raise TTSBuddyError(
                "TTS Buddy CLI reported a different download path; no file was returned.",
                code="DOWNLOAD_PATH_MISMATCH",
                job_id=job_id,
                idempotency_key=request_key,
            )
        if isinstance(byte_count, bool) or not isinstance(byte_count, int) or byte_count <= 0:
            raise TTSBuddyError(
                "TTS Buddy CLI did not report a positive audio byte count.",
                code="DOWNLOAD_METADATA_INVALID",
                job_id=job_id,
                idempotency_key=request_key,
            )
        if not isinstance(audio, dict) or str(audio.get("format", "")).lower() != "mp3":
            raise TTSBuddyError(
                "TTS Buddy CLI did not report an MP3 audio artifact.",
                code="AUDIO_FORMAT_MISMATCH",
                job_id=job_id,
                idempotency_key=request_key,
            )
        try:
            info = os.lstat(staged_path)
        except OSError:
            raise TTSBuddyError(
                "TTS Buddy CLI reported a download that is missing on disk.",
                code="DOWNLOAD_FILE_MISSING",
                job_id=job_id,
                idempotency_key=request_key,
            ) from None
        if not stat.S_ISREG(info.st_mode) or info.st_size <= 0 or info.st_size != byte_count:
            raise TTSBuddyError(
                "TTS Buddy CLI download metadata did not match the local MP3 file.",
                code="DOWNLOAD_METADATA_MISMATCH",
                job_id=job_id,
                idempotency_key=request_key,
            )

    def _error_from_payload(
        self,
        payload: dict[str, Any],
        request_key: Optional[str] = None,
        *,
        fallback_job_id: Optional[str] = None,
    ) -> TTSBuddyError:
        details = payload.get("error")
        details = details if isinstance(details, dict) else {}
        code = _safe_code(details.get("code"), "CLI_FAILED")
        server_code = _safe_code(details.get("server_code"), "")
        job_id = fallback_job_id or self._action_job_id(details.get("action")) or self._payload_job_id(payload)
        reported_key = details.get("idempotency_key")
        uncertain_codes = {"POLL_TIMEOUT", "POLL_INTERRUPTED", "REQUEST_INTERRUPTED", "DOWNLOAD_INTERRUPTED"}
        rejected_codes = {
            "AUTH_REQUIRED", "SESSION_EXPIRED", "INVALID_KEY", "USAGE_LIMIT_EXCEEDED",
            "INACTIVE_SUBSCRIPTION", "NO_API_ACCESS", "RATE_LIMITED",
        }
        idempotency_key = None
        if (
            not job_id
            and request_key
            and _SAFE_IDEMPOTENCY_KEY.fullmatch(request_key)
            and (
                server_code in uncertain_codes
                or (reported_key == request_key and server_code not in rejected_codes)
            )
        ):
            idempotency_key = request_key
        message = _friendly_error(code, server_code)
        return TTSBuddyError(
            message,
            code=server_code or code,
            job_id=job_id,
            idempotency_key=idempotency_key,
        )

    @staticmethod
    def _action_job_id(action: Any) -> Optional[str]:
        if not isinstance(action, dict) or action.get("type") not in ("status", "download"):
            return None
        argv = action.get("argv")
        if not isinstance(argv, list) or len(argv) < 3:
            return None
        if argv[0] != "ttsbuddy" or argv[1] != action.get("type"):
            return None
        return argv[2] if isinstance(argv[2], str) and _SAFE_JOB_ID.fullmatch(argv[2]) else None

    @staticmethod
    def _payload_job_id(payload: Mapping[str, Any]) -> Optional[str]:
        value = payload.get("job_id")
        return value if isinstance(value, str) and _SAFE_JOB_ID.fullmatch(value) else None


def _safe_code(value: Any, default: str) -> str:
    return value if isinstance(value, str) and _SAFE_CODE.fullmatch(value) else default


def _friendly_error(code: str, server_code: str) -> str:
    effective = server_code or code
    if effective in ("AUTH_REQUIRED", "SESSION_EXPIRED", "INVALID_KEY"):
        return "TTS Buddy authentication is unavailable or was rejected; check this Hermes profile's api_key secret or cli_config_dir."
    if effective in ("USAGE_LIMIT_EXCEEDED", "INACTIVE_SUBSCRIPTION", "NO_API_ACCESS"):
        return "TTS Buddy account access or quota does not allow this request. Check the account manually; this plugin never purchases a plan."
    if effective == "RATE_LIMITED":
        return "TTS Buddy rate-limited the request. Wait before starting another request."
    if effective in ("POLL_TIMEOUT", "POLL_INTERRUPTED", "REQUEST_INTERRUPTED", "DOWNLOAD_INTERRUPTED"):
        return "TTS Buddy stopped waiting for this request; the hosted job may still be running."
    if effective == "TTS_PROVIDER_ERROR":
        return "TTS Buddy's speech provider could not complete this request."
    return f"TTS Buddy CLI could not complete the request ({effective})."


def _recovery_guidance(*, job_id: Optional[str] = None, request_key: Optional[str] = None) -> str:
    """Build recovery text only from a validated exact job ID or generated UUID."""
    if isinstance(job_id, str) and _SAFE_JOB_ID.fullmatch(job_id):
        return (
            f" Known job ID: {job_id}. Recover only this existing job with Hermes tool "
            "`ttsbuddy_recover` using that exact ID; the tool uses this profile's current "
            "credential route and does not submit speech."
        )
    if isinstance(request_key, str) and _SAFE_IDEMPOTENCY_KEY.fullmatch(request_key):
        return (
            f" The request outcome may be uncertain. Keep idempotency key {request_key}; "
            "if retrying manually, reuse it with the same text, voice, and speed. Do not "
            "submit a new request with a different key."
        )
    return ""


def format_speed(value: float) -> str:
    """Return a stable decimal representation without locale-sensitive formatting."""
    return format(value, ".15g")


def _to_text(value: Any) -> str:
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value if isinstance(value, str) else ""
