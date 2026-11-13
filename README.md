# shrinkarr

A small, single-box Tdarr replacement. One fixed job: make every video in your library an
Apple TV friendly HEVC MP4, using the NVIDIA GPU, at the smallest size that still meets a VMAF floor.

## What it does per file

1. `ffprobe` the file. Skip if it is already HEVC in MP4 with no Dolby Vision and MP4-safe audio.
2. Already HEVC (e.g. MKV, or DV profile 8): **remux only**, no re-encode. DV RPU is stripped to plain HDR10 with `dovi_rpu=strip=1`.
3. Not HEVC: encode three 10 s samples with `hevc_nvenc` and score them with libvmaf.
   Binary-search CQ between `CQ_MIN` and `CQ_MAX` for the highest CQ whose worst sample still
   meets `VMAF_FLOOR`. Then encode the full file at that CQ.
4. Audio: AAC/AC3/EAC3/ALAC/MP3 copied; anything else (TrueHD, DTS, FLAC, PCM) to EAC3 640k, max 5.1.
5. Subtitles: text subs to `mov_text`. Image subs (PGS/VobSub), data and attachment streams are dropped (MP4 can't hold them).
6. Video tagged `hvc1` so Apple TV direct plays it.
7. Output written to a hidden temp file next to the source, verified (duration within 2 s, HEVC present,
   smaller than source when re-encoded), given the source's owner and mode, renamed into place,
   then the original is deleted.
8. Radarr/Sonarr told to rescan that movie/series.

NVDEC decodes on the GPU. If it can't decode a source (e.g. Hi10P H.264), it retries with CPU decode.

## Unraid

Requires the Nvidia Driver plugin.

```
docker run -d --name shrinkarr --runtime=nvidia \
  -e NVIDIA_VISIBLE_DEVICES=all \
  -e LIBRARIES=/media/movies,/media/tv \
  -e ARRS='radarr|http://radarr:7878|RADARR_KEY,sonarr|http://sonarr:8989|SONARR_KEY' \
  -e PATH_MAP=/media:/data/media \
  -e DRY_RUN=1 \
  -v /mnt/user/data/media:/media \
  -v /mnt/user/appdata/shrinkarr:/config \
  -p 8265:8265 \
  ghcr.io/OWNER/shrinkarr:latest
```

Start with `DRY_RUN=1`: it probes everything and logs the ffmpeg commands without touching files.
Remove it once the status page at `:8265` looks right.

## Settings (env)

| Var | Default | Meaning |
|---|---|---|
| `LIBRARIES` | `/media` | Comma-separated folders to scan |
| `STATE` | `/config/state.json` | Job history |
| `TEMP_DIR` | `/tmp` | VMAF sample scratch space |
| `SCAN_MINUTES` | `30` | Rescan interval |
| `MIN_AGE_MINUTES` | `10` | Ignore files modified more recently (still copying) |
| `VMAF_FLOOR` | `95` | Minimum VMAF on the worst sample |
| `VMAF_THREADS` | `2` | CPU threads for libvmaf |
| `CQ_MIN` / `CQ_MAX` | `18` / `32` | CQ search range |
| `ARRS` | | `kind|url|apikey`, comma-separated; kind is `radarr` or `sonarr` |
| `PATH_MAP` | | `containerPrefix:arrPrefix` if the arrs mount media at a different path |
| `DRY_RUN` | | `1` = change nothing |

Failed files are not retried until they change. To retry everything, stop the container and delete `state.json`.

## Develop

```
go test ./...
```

The end-to-end test needs `ffmpeg` with `libx265`. NVENC and VMAF paths need the container on a GPU host.
