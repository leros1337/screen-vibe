# 📹 Screen Vibe

A Go (trash) application to record the screen using ffmpeg with hardware-accelerated H265 encoding when available. Falls back to CPU encoding if no hardware encoder is detected. Output files are named with the current date and time. Logging is handled via slog. No audio is recorded. Fully made via copilot agent Claude 3.7 Sonnet.

## Features
- 🖥️ Detects GPU and selects the correct ffmpeg hardware encoder (macOS/AMD/Intel/Nvidia)
- ⚙️ Falls back to CPU encoding if no hardware encoder is available
- 🎬 Output video in H265 format with player-compatible settings
- 📁 Output and log files stored in the "output" directory with current date and time as filenames, restarts recording after file size reach limit
- 📄 Uses slog for logging
- 📼 Produces MKV files compatible with most media players (best played with VLC)
- 🔍 Checks ffmpeg availability and exits if not found
- 🖥️ On macOS/Windows, extracts the main display device ID (not camera/other)
- 🔇 No audio recording

## Usage
1. Ensure ffmpeg is installed and available in your PATH.
2. Build the project:
   ```sh
   go build -o screen-vibe
   ```
3. Run the application:
   ```sh
   ./screen-vibe
   ```

### Command-line Options
- `-size`: Specify the maximum file size in megabytes (default: 1024, which is 1GB)
   ```sh
   # Example: Set maximum file size to 500MB
   ./screen-vibe -size 500
   ```
   
- `-display`: Manually specify which display to record (default: auto-detect)
   ```sh
   # macOS example: Record display with ID 1
   ./screen-vibe -display "1:none"
   
   # Windows example: Record a specific window by title
   ./screen-vibe -display "title=My Window Title"
   
   # Linux example: Record secondary monitor
   ./screen-vibe -display ":0.0+1920,0"
   ```
   
- `-list`: Show available displays and exit without recording
   ```sh
   # List all available displays
   ./screen-vibe -list
   ```
   
- `-fps`: Specify frames per second for recording (default: 5)
   ```sh
   # Example: Record at 15 frames per second
   ./screen-vibe -fps 15
   
   # Example: Record at 30 frames per second with 500MB size limit
   ./screen-vibe -fps 30 -size 500
   ```

- `-h264`: Use H.264 codec instead of H.265/HEVC for better compatibility
   ```sh
   # Example: Record using H.264 for better player compatibility (especially on Windows)
   ./screen-vibe -h264
   ```
   
- `-bitrate`: Specify the video bitrate in kbit/s (default: 700)
   ```sh
   # Example: Record with higher quality (2000 kbit/s)
   ./screen-vibe -bitrate 2000
   
   # Example: Record with lower quality for smaller file size (300 kbit/s)
   ./screen-vibe -bitrate 300
   ```

- `-preset`: Specify encoding preset (default: medium)
   ```sh
   # Example: Use "faster" preset for lower CPU usage
   ./screen-vibe -preset faster
   
   # Example: Use "slow" preset for better quality
   ./screen-vibe -preset slow
   
   # Available presets: ultrafast, superfast, veryfast, faster, fast, medium, slow, slower
   ```

## Requirements

### All Platforms
- Go 1.20+
- ffmpeg installed with H.265/HEVC support

### macOS
- ffmpeg with VideoToolbox support (default in Homebrew installation)
  ```sh
  brew install ffmpeg
  ```

### Windows
- ffmpeg added to the system PATH
  ```sh
  winget install ffmpeg
  ```
- For hardware acceleration:
  - NVIDIA GPU: ffmpeg with NVENC support
  - Intel GPU: ffmpeg with QuickSync support
  - AMD GPU: ffmpeg with AMF support

### Linux
- ffmpeg with appropriate hardware acceleration support:
  - NVIDIA GPU: ffmpeg with NVENC support
  - Intel GPU: ffmpeg with QSV support
  - AMD GPU: ffmpeg with AMF support
- X11 for screen capture

## ⚠️ Important Notes

- **Remote Desktop Users**: If you're using Screen Vibe via Remote Desktop Protocol (RDP), disconnecting from the RDP session will interrupt the recording. This is a limitation of how screen capturing works through remote sessions. Working fine using vlc/rustdesk.

- **Video Playback**: For best results, use [VLC media player](https://www.videolan.org/vlc/) to open the recorded MKV files. Some default media players may not support all video configurations.

- **Background Service** 🔄: To run Screen Vibe as a background service on Windows, use [NSSM (Non-Sucking Service Manager)](https://nssm.cc/). NSSM provides better control over service restarts and throttling compared to standard Windows services.

  ```sh
  # Install as a service (run as Administrator)
  nssm install ScreenVibe "C:\path\to\screen-vibe.exe" "-bitrate 700 -fps 5"
  
  # Configure automatic restart and throttling
  nssm set ScreenVibe AppThrottle 10000
  nssm set ScreenVibe AppExit 0 Restart
  nssm set ScreenVibe AppStdout "C:\path\to\logs\service.log"
  nssm set ScreenVibe AppStderr "C:\path\to\logs\service.log"
  
  # Start the service
  nssm start ScreenVibe
  ```

## License
📄 MIT
