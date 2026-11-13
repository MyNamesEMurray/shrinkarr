# Shrinkarr

Shrinkarr makes your video library smaller and easier to play. It re-encodes video with your
GPU at a quality you can't tell apart from the original. It is a simpler, self-hosted take on Tdarr.

- **Templates for non-tinkerers.** Pick one in the setup wizard and you're done.
- **Visual flow editor** for everyone else. Drag blocks, connect them, test on a real file.
- **Auto quality.** Encodes short samples and picks the smallest file that still scores your
  VMAF target (95 ≈ visually identical), like Alchemist/ab-av1.
- **Any hardware.** NVIDIA NVENC, Intel QuickSync, AMD/Intel VAAPI, or CPU. Detected at startup.
- **Safe by default.** Starts in test mode. The original is only replaced after the new file is checked
  (length matches, video present). It can be moved to a folder instead of being deleted.
- **Stats** for space saved per day, per library and per codec.
- Queue with live progress, pause, cancel, retry, ignore. Quiet hours. Radarr/Sonarr rescan.
  Discord alerts. Optional password.

## Install on Unraid

1. NVIDIA only: install the **Nvidia Driver** plugin.
2. Docker → Add Container → Template URL:
   `https://raw.githubusercontent.com/MyNamesEMurray/shrinkarr/main/unraid/shrinkarr.xml`
3. Set **Media** to your media share.
   - NVIDIA: add `--runtime=nvidia` to Extra Parameters and set `NVIDIA_VISIBLE_DEVICES=all`.
   - Intel or AMD: set **Intel/AMD GPU** to `/dev/dri`.
4. Open the web UI on port 8265 and follow the wizard.

## Docker

```
docker run -d --name shrinkarr \
  -p 8265:8265 \
  -v /path/to/media:/media \
  -v /path/to/appdata/shrinkarr:/config \
  -v /path/to/appdata/shrinkarr/tmp:/tmp \
  --runtime=nvidia -e NVIDIA_VISIBLE_DEVICES=all \
  ghcr.io/mynamesemurray/shrinkarr:latest
```

For Intel or AMD, replace the `--runtime=nvidia …` line with `--device /dev/dri`.
Add `-e TZ=America/Detroit` (your zone) so quiet hours and daily stats use local time.
Unraid sets `TZ` for you.

## Templates

| Template | What it does |
|---|---|
| Save space, keep everything (HEVC) | Re-encodes older codecs to HEVC. Keeps all tracks and the file type. Recommended. |
| Apple TV / Plex direct play (HEVC MP4) | HEVC in MP4 with `hvc1` tag, EAC3 audio, Dolby Vision reduced to HDR10. HEVC files are only repackaged. |
| Repackage to MP4 (no re-encode) | Lossless and fast. Converts only audio/subtitles MP4 can't hold. |
| Plays anywhere (H.264 1080p MP4) | H.264, up to 1080p, AAC plus a stereo track. Skips HDR. |
| Smallest files (AV1) | AV1. Best with RTX 40 / Arc / RX 7000. |

Templates are read-only. Press **Duplicate** to make your own version.

## How flows work

Each file enters at **Start** and follows the arrows. **Checks** (codec, HDR, resolution, bitrate,
size, name…) pick an output. **Video / Audio / Subtitle** blocks collect settings. **Run FFmpeg**
does the work into a hidden temp file next to the original. **Replace original** swaps it in.
Blocks after Run FFmpeg (like **Saved at least…**) look at the new file.

Use **Test on a file** in the editor to see the path a file takes and the exact ffmpeg command.
Nothing is changed.

## Sign-in

Off by default. Turn on either or both in Settings:

- **Password:** one shared password.
- **Single sign-on (OIDC):** Authentik, Authelia, Keycloak, Pocket ID or any OpenID Connect provider.
  In Authentik: create an OAuth2/OpenID provider (confidential), set the redirect URI to
  `https://<your shrinkarr address>/api/oidc/callback`, and create an application for it. Copy the
  issuer URL, client ID and secret into Settings. Control who gets in by binding users or groups to
  the application, or with Shrinkarr's "Allowed users or groups" list.

Already using Authentik forward auth in your reverse proxy (Caddy, Traefik, NPM)? That works too,
with Shrinkarr's own sign-in off.

Locked out? Start the container with `SHRINKARR_DISABLE_AUTH=1`, fix Settings, then remove it.

## Files

`/config` holds `settings.json`, `flows/`, `files.json` (latest state per file) and
`history.jsonl` (every job, used for stats). Back it up with your appdata.

## Develop

```
go test ./...                     # needs ffmpeg with libx265 for the end-to-end tests
CONFIG_DIR=./cfg go run .         # http://localhost:8265
```

Backend: Go standard library only. Frontend: plain JS in `web/`, with [Drawflow](https://github.com/jerosoler/Drawflow) (MIT) for the editor.
