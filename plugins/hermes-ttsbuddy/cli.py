"""Terminal-only account-deletion delegation to the TTS Buddy CLI.

This module deliberately owns no deletion state or API client. The selected CLI
and its explicit config directory remain the source of truth for every stage.
"""

from __future__ import annotations

import argparse
import getpass
import json
import re
import sys
import warnings
from pathlib import Path
from typing import Any, Optional


_MIN_CLI_VERSION = (0, 16, 0)
_PROBE_TIMEOUT_SECONDS = 5
_DELETE_TIMEOUT_SECONDS = 180
_MAX_CODE_STDIN_BYTES = 8
_CHALLENGE_ID = re.compile(r"^[a-f0-9]{32}$")
_VERSION = re.compile(r"^v?(\d+)\.(\d+)\.(\d+)(?:\+[0-9A-Za-z.-]+)?$")
_SAFE_CODE = re.compile(r"^[A-Z][A-Z0-9_]{0,63}$")
_SAFE_STATE = re.compile(r"^[a-z][a-z0-9_]{0,63}$")
_UUID = re.compile(r"^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$")
_SAFE_TIMESTAMP = re.compile(r"^[0-9T:+.Z-]{1,40}$")
_SAFE_SOURCES = frozenset({
    "local_continuation", "local_completed_receipt", "server_response", "unverified_response",
})
_APPLE_AUTHORIZATION = frozenset({
    "not_linked", "revoked", "already_revoked_by_apple", "manual_action_required",
})
_DELETION_WARNING = (
    "Permanent account deletion removes cloud files, credentials, and identity, "
    "and cancels subscriptions. Local downloaded files remain."
)


class _SafeArgumentParser(argparse.ArgumentParser):
    """Argparse errors never echo unknown tokens that could contain a code."""

    def error(self, _message: str) -> None:
        _safe_parser_error()


def _safe_parser_error() -> None:
    print(
        "Invalid TTS Buddy account command arguments. Use `hermes ttsbuddy account delete --help`.",
        file=sys.stderr,
    )
    raise SystemExit(2)


def setup_ttsbuddy_cli(parser: argparse.ArgumentParser) -> None:
    """Capture the plugin argument tail so Hermes cannot echo unknown code-like tokens."""
    parser.error = lambda _message: _safe_parser_error()
    parser.add_argument("_ttsbuddy_args", nargs=argparse.REMAINDER, help=argparse.SUPPRESS)
    parse_known_args = parser.parse_known_args

    def _capture_plugin_unknowns(args=None, namespace=None):
        parsed, extras = parse_known_args(args, namespace)
        if extras:
            parsed._ttsbuddy_args = [*getattr(parsed, "_ttsbuddy_args", []), *extras]
        # The host root parser must not format these tokens; the local safe parser
        # reports one constant error without exposing any unknown value.
        return parsed, []

    parser.parse_known_args = _capture_plugin_unknowns

def _deletion_parser() -> argparse.ArgumentParser:
    parser = _SafeArgumentParser(prog="hermes ttsbuddy", description="Terminal-only TTS Buddy account deletion")
    top = parser.add_subparsers(dest="ttsbuddy_area", required=True, parser_class=_SafeArgumentParser)
    account = top.add_parser("account", help="Manage the account using the installed TTS Buddy CLI")
    account_sub = account.add_subparsers(dest="account_command", required=True, parser_class=_SafeArgumentParser)
    deletion = account_sub.add_parser("delete", help="Prepare, confirm, inspect, or cancel account deletion")
    stages = deletion.add_subparsers(dest="deletion_action", required=True, parser_class=_SafeArgumentParser)

    start = stages.add_parser("start", help="Prepare fresh email verification")
    start.add_argument("--email", help="Existing account email address")
    start.add_argument("--json", action="store_true", help=argparse.SUPPRESS)

    confirm = stages.add_parser("confirm", help="Explicitly submit or retry permanent deletion")
    confirm.add_argument("--challenge-id", help="Challenge ID returned by start")
    confirm.add_argument("--confirm-email", help="Exact email address used by start")
    confirm.add_argument("--yes", action="store_true",
                         help="Acknowledge permanent deletion and subscription cancellation")
    confirm.add_argument("--code-stdin", action="store_true",
                         help="Read a bounded verification code from stdin instead of prompting")
    confirm.add_argument(
        "--apple-manual-fallback-confirmed", action="store_true",
        help="Confirm you will manually revoke Sign in with Apple if automatic revocation is unavailable",
    )
    confirm.add_argument("--json", action="store_true", help=argparse.SUPPRESS)

    status = stages.add_parser("status", help="Inspect the existing deletion outcome; never submits deletion")
    status.add_argument("--challenge-id", help="Challenge ID returned by start")
    status.add_argument("--code-stdin", action="store_true",
                        help="Optionally verify a fresh email code through bounded stdin")
    status.add_argument("--json", action="store_true", help=argparse.SUPPRESS)

    cancel = stages.add_parser("cancel", help="Cancel an unused deletion challenge")
    cancel.add_argument("--challenge-id", help="Challenge ID returned by start")
    cancel.add_argument("--json", action="store_true", help=argparse.SUPPRESS)
    return parser


def account_slash_command(raw_args: str) -> str:
    """Provide fixed help only; chat input is never parsed as deletion instructions."""
    del raw_args
    return (
        "Account deletion is available only from the local Hermes terminal with "
        "`hermes ttsbuddy account delete start|confirm|status|cancel`. It requires a "
        "fresh email code and an explicitly configured persistent `cli_config_dir`; "
        "speech keys and CLI sessions do not authorize deletion. This chat command "
        "cannot start deletion or accept email codes. Do not send verification codes in chat."
    )


def run_ttsbuddy_cli(provider: Any, args: argparse.Namespace) -> int:
    """Run one explicit account-deletion CLI stage and emit a sanitized JSON receipt."""
    try:
        parsed = _deletion_parser().parse_args(getattr(args, "_ttsbuddy_args", []))
    except SystemExit as exc:
        return int(exc.code or 0)
    return _run_deletion_stage(provider, parsed)


def _run_deletion_stage(provider: Any, args: argparse.Namespace) -> int:
    from .provider import TTSBuddyError, _CommandTimeout

    action = getattr(args, "deletion_action", None)
    if action not in {"start", "confirm", "status", "cancel"}:
        return _emit_error("INVALID_COMMAND", "Choose start, confirm, status, or cancel.", exit_code=2)

    challenge_id = getattr(args, "challenge_id", None)
    if action in {"confirm", "status", "cancel"}:
        if not isinstance(challenge_id, str) or not _CHALLENGE_ID.fullmatch(challenge_id):
            return _emit_error("INVALID_CHALLENGE_ID", "Provide the exact challenge ID returned by start.", exit_code=2)
    if action == "confirm":
        confirm_email = getattr(args, "confirm_email", None)
        if getattr(args, "yes", False) is not True or not isinstance(confirm_email, str) or not confirm_email:
            return _emit_error(
                "CONFIRMATION_REQUIRED",
                "Confirm requires the exact --confirm-email value and explicit --yes.",
                exit_code=2,
                challenge_id=challenge_id,
            )
    if action == "start":
        email = getattr(args, "email", None)
        if not isinstance(email, str) or not email or len(email) > 254:
            return _emit_error("EMAIL_REQUIRED", "Provide an existing account email with --email.", exit_code=2)

    config_dir = _explicit_config_dir(provider)
    if config_dir is None:
        return _emit_error(
            "CLI_CONFIG_DIR_REQUIRED",
            "Account deletion requires an existing absolute cli_config_dir selected in this Hermes profile. "
            "Speech API keys and temporary CLI config directories cannot authorize deletion.",
            exit_code=2,
            challenge_id=challenge_id,
        )

    executable = provider._resolve_executable()
    if not executable:
        return _emit_error("CLI_NOT_FOUND", "The configured TTS Buddy CLI executable was not found.",
                           exit_code=2, challenge_id=challenge_id)

    env = provider._child_env(None)
    try:
        version_result = provider._run_cli(
            [executable, "--json", "version"], env=env, timeout=_PROBE_TIMEOUT_SECONDS,
        )
    except _CommandTimeout:
        return _emit_error("CLI_VERSION_UNAVAILABLE", "The TTS Buddy CLI version check timed out.",
                           exit_code=1, challenge_id=challenge_id)
    except KeyboardInterrupt:
        return _emit_error("INTERRUPTED", "The CLI version check was interrupted; no deletion command was run.",
                           exit_code=130, challenge_id=challenge_id)
    except Exception:
        return _emit_error("CLI_VERSION_UNAVAILABLE", "Could not verify the TTS Buddy CLI version.",
                           exit_code=1, challenge_id=challenge_id)

    version = _parse_cli_version(version_result.stdout) if version_result.returncode == 0 else None
    if version is None or version < _MIN_CLI_VERSION:
        return _emit_error(
            "CLI_VERSION_UNSUPPORTED",
            "Account deletion requires TTS Buddy CLI 0.16.0 or newer. Speech features remain available on older CLI versions.",
            exit_code=2,
            challenge_id=challenge_id,
        )

    input_text: Optional[str] = None
    argv = [executable, "--json", "--config-dir", config_dir, "account", "delete", action]
    if action == "start":
        argv.extend(("--email", args.email))
    else:
        argv.extend(("--challenge-id", challenge_id))
    if action == "confirm":
        argv.extend(("--confirm-email", args.confirm_email, "--yes", "--code-stdin"))
        if getattr(args, "apple_manual_fallback_confirmed", False):
            argv.append("--apple-manual-fallback-confirmed")
        code, error = _confirmation_code(explicit_stdin=getattr(args, "code_stdin", False))
        if error:
            return _emit_error(error[0], error[1], exit_code=130 if error[0] == "INTERRUPTED" else 2,
                               challenge_id=challenge_id)
        input_text = f"{code}\n" if code else ""
    elif action == "status" and getattr(args, "code_stdin", False):
        argv.append("--code-stdin")
        code, error = _bounded_stdin_code()
        if error:
            return _emit_error(error[0], error[1], exit_code=2, challenge_id=challenge_id)
        input_text = f"{code}\n" if code else ""

    try:
        result = provider._run_cli(
            argv, env=env, timeout=_DELETE_TIMEOUT_SECONDS, input_text=input_text,
        )
    except _CommandTimeout as exc:
        receipt = _sanitize_receipt(exc.stdout, action=action, fallback_challenge_id=challenge_id)
        receipt["error_code"] = "CLI_TIMEOUT"
        if receipt.get("success") is True and receipt.get("deleted") is True:
            receipt["message"] = (
                "The CLI timed out after returning a verified completion receipt. "
                "Deletion is complete; retain this receipt and review local authentication state."
            )
        else:
            receipt["success"] = False
            receipt["deleted"] = False
            receipt["state"] = "unknown"
            receipt["message"] = (
                "The CLI timed out. No command retry was attempted; inspect status using the same CLI config directory."
            )
        _add_next_action(receipt, challenge_id)
        _emit(receipt)
        return 1
    except KeyboardInterrupt:
        receipt = _empty_receipt(action, challenge_id)
        receipt.update({
            "error_code": "INTERRUPTED",
            "message": "The CLI was interrupted. No automatic retry was attempted; inspect status using the same CLI config directory.",
        })
        _add_next_action(receipt, challenge_id)
        _emit(receipt)
        return 130
    except TTSBuddyError as exc:
        code = exc.code if _SAFE_CODE.fullmatch(exc.code or "") else "CLI_FAILED"
        receipt = _empty_receipt(action, challenge_id)
        receipt.update({"error_code": code, "message": "Could not run the TTS Buddy CLI command."})
        _add_next_action(receipt, challenge_id)
        _emit(receipt)
        return 1
    except Exception:
        receipt = _empty_receipt(action, challenge_id)
        receipt.update({"error_code": "CLI_FAILED", "message": "Could not run the TTS Buddy CLI command."})
        _add_next_action(receipt, challenge_id)
        _emit(receipt)
        return 1

    receipt = _sanitize_receipt(result.stdout, action=action, fallback_challenge_id=challenge_id)
    if result.returncode != 0:
        _add_next_action(receipt, challenge_id)
        _emit(receipt)
        return result.returncode
    if not _successful_receipt(action, receipt):
        receipt["success"] = False
        receipt["deleted"] = False
        receipt.setdefault("error_code", "CLI_INCOMPLETE_RESPONSE")
        receipt.setdefault("message", "The CLI did not confirm a completed deletion operation.")
        _add_next_action(receipt, challenge_id)
        _emit(receipt)
        return 1
    _add_next_action(receipt, challenge_id)
    _emit(receipt)
    return 0


def _successful_receipt(action: str, receipt: dict[str, Any]) -> bool:
    challenge_id = receipt.get("challenge_id")
    if receipt.get("success") is not True or not isinstance(challenge_id, str) or not _CHALLENGE_ID.fullmatch(challenge_id):
        return False
    state = receipt.get("state")
    if action == "start":
        # start can return an existing protected continuation after a prior attempt.
        # It must never treat a completed receipt as a fresh challenge.
        return state in {
            "verification_required", "ready", "unknown", "requested", "running", "failed", "none",
        } and receipt.get("deleted") is False
    if action in {"confirm", "status"}:
        return state == "completed" and receipt.get("deleted") is True
    return action == "cancel" and state == "cancelled" and receipt.get("deleted") is False


def _explicit_config_dir(provider: Any) -> Optional[str]:
    """Resolve only the profile's explicit, existing config directory; never create a temp route."""
    try:
        configured = provider._setting("cli_config_dir", "")
        if not isinstance(configured, str) or not configured.strip():
            return None
        path = Path(configured.strip()).expanduser()
        if not path.is_absolute():
            return None
        resolved = path.resolve(strict=True)
        return str(resolved) if resolved.is_dir() else None
    except Exception:
        return None


def _parse_cli_version(stdout: str) -> Optional[tuple[int, int, int]]:
    try:
        payload = json.loads(stdout)
        value = payload.get("version") if isinstance(payload, dict) else None
        if not isinstance(value, str):
            return None
        match = _VERSION.fullmatch(value.strip())
        return tuple(int(part) for part in match.groups()) if match else None
    except (ValueError, TypeError, AttributeError):
        return None


def _confirmation_code(*, explicit_stdin: bool) -> tuple[str, Optional[tuple[str, str]]]:
    if explicit_stdin:
        return _bounded_stdin_code()
    if not (_is_tty(sys.stdin) and _is_tty(sys.stderr)):
        return "", ("CODE_INPUT_REQUIRED", "Use a terminal for hidden code entry or pass --code-stdin with protected stdin.")
    try:
        with warnings.catch_warnings():
            warnings.simplefilter("error", getpass.GetPassWarning)
            value = getpass.getpass("Email verification code (press Enter to reuse verified proof): ")
    except (EOFError, getpass.GetPassWarning):
        return "", ("HIDDEN_INPUT_UNAVAILABLE", "Hidden verification-code input is unavailable; retry with --code-stdin.")
    except KeyboardInterrupt:
        return "", ("INTERRUPTED", "Verification-code entry was interrupted; no deletion command was run.")
    return _validate_code_text(value)


def _bounded_stdin_code() -> tuple[str, Optional[tuple[str, str]]]:
    stream = getattr(sys.stdin, "buffer", sys.stdin)
    try:
        value = stream.read(_MAX_CODE_STDIN_BYTES + 1)
    except Exception:
        return "", ("CODE_INPUT_UNAVAILABLE", "Could not read the bounded verification-code input.")
    if isinstance(value, str):
        try:
            raw = value.encode("ascii")
        except UnicodeEncodeError:
            return "", ("INVALID_CODE_INPUT", "Provide exactly six digits, optionally followed by a newline.")
    elif isinstance(value, bytes):
        raw = value
    else:
        return "", ("INVALID_CODE_INPUT", "Provide exactly six digits, optionally followed by a newline.")
    if len(raw) > _MAX_CODE_STDIN_BYTES:
        return "", ("INVALID_CODE_INPUT", "Provide exactly six digits, optionally followed by a newline.")
    try:
        text = raw.decode("ascii")
    except UnicodeDecodeError:
        return "", ("INVALID_CODE_INPUT", "Provide exactly six digits, optionally followed by a newline.")
    if text.endswith("\r\n"):
        text = text[:-2]
    elif text.endswith("\n"):
        text = text[:-1]
    return _validate_code_text(text)


def _validate_code_text(value: str) -> tuple[str, Optional[tuple[str, str]]]:
    if value == "":
        # The CLI alone knows whether a verified proof is already retained.
        return "", None
    if not re.fullmatch(r"[0-9]{6}", value):
        return "", ("INVALID_CODE_INPUT", "Provide exactly six digits, optionally followed by a newline.")
    return value, None


def _is_tty(stream: Any) -> bool:
    try:
        return stream.isatty() is True
    except Exception:
        return False


def _empty_receipt(action: str, challenge_id: Optional[str]) -> dict[str, Any]:
    receipt: dict[str, Any] = {"success": False, "deleted": False, "warning": _DELETION_WARNING}
    if action in {"confirm", "status", "cancel"}:
        receipt["state"] = "unknown"
    if isinstance(challenge_id, str) and _CHALLENGE_ID.fullmatch(challenge_id):
        receipt["challenge_id"] = challenge_id
    return receipt


def _sanitize_receipt(stdout: str, *, action: str, fallback_challenge_id: Optional[str]) -> dict[str, Any]:
    try:
        payload = json.loads(stdout)
    except (ValueError, TypeError):
        payload = None
    if not isinstance(payload, dict):
        receipt = _empty_receipt(action, fallback_challenge_id)
        receipt.update({"error_code": "CLI_RESPONSE_INVALID", "message": "The CLI returned no valid deletion receipt."})
        _add_next_action(receipt, fallback_challenge_id)
        return receipt

    receipt: dict[str, Any] = {"warning": _DELETION_WARNING}
    success = payload.get("success")
    receipt["success"] = success if isinstance(success, bool) else False
    state = payload.get("state")
    receipt["state"] = state if isinstance(state, str) and _SAFE_STATE.fullmatch(state) else "unknown"
    deleted = payload.get("deleted")
    receipt["deleted"] = bool(deleted is True and receipt["success"] is True and receipt["state"] == "completed")

    payload_challenge = payload.get("challenge_id")
    response_challenge_valid = isinstance(payload_challenge, str) and bool(_CHALLENGE_ID.fullmatch(payload_challenge))
    challenge = payload_challenge if response_challenge_valid else None
    if not challenge:
        challenge = fallback_challenge_id if isinstance(fallback_challenge_id, str) and _CHALLENGE_ID.fullmatch(fallback_challenge_id) else None
    if fallback_challenge_id and response_challenge_valid and payload_challenge != fallback_challenge_id:
        receipt.update({
            "success": False, "deleted": False, "state": "unknown",
            "error_code": "CLI_RESPONSE_MISMATCH",
            "message": "The CLI receipt did not match the requested challenge.",
            "challenge_id": fallback_challenge_id,
        })
        return receipt
    if payload.get("success") is True and not response_challenge_valid:
        receipt.update({
            "success": False, "deleted": False, "state": "unknown",
            "error_code": "CLI_RESPONSE_INVALID",
            "message": "The CLI returned a success response without a valid challenge ID.",
        })
    if challenge:
        receipt["challenge_id"] = challenge

    source = payload.get("source")
    if isinstance(source, str) and source in _SAFE_SOURCES:
        receipt["source"] = source
    for key in ("request_id", "correlation_id"):
        value = payload.get(key)
        if isinstance(value, str) and _UUID.fullmatch(value):
            receipt[key] = value
    for key in ("expires_at", "verified_at"):
        value = payload.get(key)
        if isinstance(value, str) and _SAFE_TIMESTAMP.fullmatch(value):
            receipt[key] = value
    if payload.get("expiry_scope") == "cli_continuation_deadline":
        receipt["expiry_scope"] = "cli_continuation_deadline"
    apple = payload.get("apple_authorization")
    if isinstance(apple, str) and apple in _APPLE_AUTHORIZATION:
        receipt["apple_authorization"] = apple
    if payload.get("local_credentials") == "preserved; ownership is not recorded":
        receipt["local_credentials"] = "preserved; ownership is not recorded"
    for key in ("retryable", "partial_cleanup", "local_receipt_saved"):
        if isinstance(payload.get(key), bool):
            receipt[key] = payload[key]
    for key in ("http_status", "pending_count", "retry_after_seconds"):
        value = payload.get(key)
        if isinstance(value, int) and not isinstance(value, bool) and 0 <= value <= 1_000_000:
            receipt[key] = value

    error = payload.get("error")
    raw_error_code = payload.get("error_code")
    if not isinstance(raw_error_code, str) and isinstance(error, dict):
        raw_error_code = error.get("reason") or error.get("code")
    if isinstance(raw_error_code, str) and _SAFE_CODE.fullmatch(raw_error_code):
        receipt["error_code"] = raw_error_code
    raw_category = payload.get("error_category")
    if isinstance(raw_category, str) and _SAFE_STATE.fullmatch(raw_category):
        receipt["error_category"] = raw_category
    if receipt.get("state") == "completed" and receipt.get("deleted") is True \
            and payload.get("local_receipt_saved") is False:
        receipt["cleanup_warning"] = (
            "The CLI could not save its local receipt. Retain this verified completion receipt, "
            "review it, then remove temporary authentication state from the private CLI config directory."
        )
    if receipt["state"] != "completed" and receipt.get("success") is False and "error_code" not in receipt:
        if isinstance(error, dict) and isinstance(error.get("code"), str) and _SAFE_CODE.fullmatch(error["code"]):
            receipt["error_code"] = error["code"]
    _add_next_action(receipt, challenge)
    return receipt


def _add_next_action(receipt: dict[str, Any], challenge_id: Optional[str]) -> None:
    if receipt.get("state") == "completed":
        return
    if receipt.get("state") == "cancelled":
        receipt["next_action"] = "The challenge was cancelled. Start again only if account deletion is still explicitly requested."
    elif receipt.get("state") in {"verification_required", "ready"} and isinstance(challenge_id, str) and _CHALLENGE_ID.fullmatch(challenge_id):
        receipt["next_action"] = (
            f"Continue with hermes ttsbuddy account delete confirm --challenge-id {challenge_id} "
            "--confirm-email <exact prepared email> --yes. Enter the code only at the hidden terminal prompt."
        )
    elif isinstance(challenge_id, str) and _CHALLENGE_ID.fullmatch(challenge_id):
        receipt["next_action"] = (
            f"Inspect challenge {challenge_id} with hermes ttsbuddy account delete status "
            "using the same CLI config directory. Retry confirm only with explicit approval."
        )
    elif receipt.get("state") == "unknown":
        receipt["next_action"] = "Do not retry automatically. Check the selected CLI config directory and contact support if the outcome is unresolved."


def _emit_error(
    code: str,
    message: str,
    *,
    exit_code: int,
    challenge_id: Optional[str] = None,
) -> int:
    receipt = _empty_receipt("confirm" if challenge_id else "start", challenge_id)
    receipt.update({"error_code": code, "message": message})
    _add_next_action(receipt, challenge_id)
    _emit(receipt)
    return exit_code


def _emit(receipt: dict[str, Any]) -> None:
    # All fields are created from a fixed allowlist and bounded identifiers above.
    print(json.dumps(receipt, ensure_ascii=True, separators=(",", ":")))
