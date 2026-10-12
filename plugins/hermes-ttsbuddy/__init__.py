"""Native Hermes Agent provider for the TTS Buddy CLI."""

from .provider import TTSBuddyProvider


def register(ctx):
    """Register the profile-scoped TTS Buddy provider with Hermes."""
    provider = TTSBuddyProvider(ctx)
    ctx.register_tts_provider(provider)
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
