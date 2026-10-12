"""Native Hermes Agent provider for the TTS Buddy CLI."""

from functools import partial

from .cli import account_slash_command, run_ttsbuddy_cli, setup_ttsbuddy_cli
from .provider import TTSBuddyProvider


def register(ctx):
    """Register the profile-scoped TTS Buddy provider with Hermes."""
    provider = TTSBuddyProvider(ctx)
    ctx.register_tts_provider(provider)
    ctx.register_cli_command(
        name="ttsbuddy",
        help="Manage TTS Buddy account deletion from the local terminal",
        description=(
            "Terminal-only account deletion delegated to the installed TTS Buddy CLI. "
            "Requires fresh email verification and an explicit persistent cli_config_dir."
        ),
        setup_fn=setup_ttsbuddy_cli,
        handler_fn=partial(run_ttsbuddy_cli, provider),
    )
    ctx.register_command(
        "ttsbuddy-account",
        handler=account_slash_command,
        description="Show safe terminal guidance for TTS Buddy account deletion.",
    )
    ctx.register_tool(
        name="ttsbuddy_recover",
        toolset="tts",
        schema={
            "name": "ttsbuddy_recover",
            "description": (
                "Recover audio for a previously returned TTS Buddy job ID. This checks/downloads only "
                "that exact existing job and never submits speech."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "job_id": {"type": "string", "description": "Exact TTS Buddy job ID from the earlier request"},
                    "output_path": {
                        "type": "string",
                        "description": "New output path; the provider writes an MP3 and refuses to overwrite an existing file",
                    },
                },
                "required": ["job_id", "output_path"],
            },
        },
        handler=provider.recover_tool,
        check_fn=provider.is_available,
        description="Download a known TTS Buddy job using this Hermes profile's selected credentials.",
        emoji="📥",
    )
