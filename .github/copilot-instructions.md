<!-- Use this file to provide workspace-specific custom instructions to Copilot. For more details, visit https://code.visualstudio.com/docs/copilot/copilot-customization#_use-a-githubcopilotinstructionsmd-file -->

This project is a Go application for screen recording using ffmpeg. Requirements:
- Detect GPU and select the correct ffmpeg hardware encoder (macOS/AMD/Intel/Nvidia).
- Fallback to CPU encoding if no hardware encoder is available.
- Output H265 video, filename and log file as current date/time.
- Use slog for logging.
- Check ffmpeg availability and exit if not found.
- On macOS/Windows, extract the main display device ID (not camera/other).
- Do not record audio.
