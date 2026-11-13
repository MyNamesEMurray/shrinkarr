package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func yes(b bool) int {
	if b {
		return 1
	}
	return 2
}

func init() {
	register(&NodeDef{Type: "start", Label: "Start", Category: "start",
		Help: "Every file in the library enters the flow here.", Outputs: []string{"File"},
		Run: func(j *Job, p Params) (int, error) { return 1, nil }})

	// --- checks ---
	register(&NodeDef{Type: "check_codec", Label: "Video codec is…", Category: "check",
		Help:    "Checks the video codec. Common names: hevc (H.265), h264, av1, mpeg4, vc1.",
		Outputs: []string{"Yes", "No"},
		Params:  []Param{{Key: "codecs", Label: "Codecs", Kind: "list", Default: "hevc"}},
		Run: func(j *Job, p Params) (int, error) {
			v := j.Probe.Video()
			return yes(v != nil && slices.Contains(p.List("codecs"), v.CodecName)), nil
		}})
	register(&NodeDef{Type: "check_container", Label: "Container is…", Category: "check",
		Help: "Checks the file type: mp4 or mkv.", Outputs: []string{"Yes", "No"},
		Params: []Param{{Key: "containers", Label: "Containers", Kind: "list", Default: "mp4"}},
		Run: func(j *Job, p Params) (int, error) {
			return yes(slices.Contains(p.List("containers"), j.Probe.Container())), nil
		}})
	register(&NodeDef{Type: "check_hdr", Label: "Is HDR?", Category: "check",
		Help: "HDR10, HLG or Dolby Vision.", Outputs: []string{"HDR", "SDR"},
		Run: func(j *Job, p Params) (int, error) {
			v := j.Probe.Video()
			return yes(v != nil && v.HDR()), nil
		}})
	register(&NodeDef{Type: "check_dv", Label: "Has Dolby Vision?", Category: "check",
		Outputs: []string{"Yes", "No"},
		Run: func(j *Job, p Params) (int, error) {
			v := j.Probe.Video()
			return yes(v != nil && v.DV()), nil
		}})
	register(&NodeDef{Type: "check_resolution", Label: "Resolution at least…", Category: "check",
		Help: "Compares the video height. 2160 = 4K, 1080 = Full HD.", Outputs: []string{"At least", "Smaller"},
		Params: []Param{{Key: "height", Label: "Height", Kind: "select", Options: []string{"2160", "1440", "1080", "720", "480"}, Default: "2160"}},
		Run: func(j *Job, p Params) (int, error) {
			v := j.Probe.Video()
			return yes(v != nil && float64(v.effHeight()) >= p.F("height")*0.95), nil
		}})
	register(&NodeDef{Type: "check_bitrate", Label: "Bitrate above…", Category: "check",
		Help: "Video bitrate in megabits per second. Low-bitrate files gain little from re-encoding.", Outputs: []string{"Above", "Below"},
		Params: []Param{{Key: "mbps", Label: "Mbps", Kind: "number", Default: 4.0}},
		Run:    func(j *Job, p Params) (int, error) { return yes(j.Probe.Mbps() > p.F("mbps")), nil }})
	register(&NodeDef{Type: "check_size", Label: "File size above…", Category: "check",
		Outputs: []string{"Above", "Below"},
		Params:  []Param{{Key: "gb", Label: "GB", Kind: "number", Default: 2.0}},
		Run: func(j *Job, p Params) (int, error) {
			fi, err := os.Stat(j.Cur)
			if err != nil {
				return 0, err
			}
			return yes(float64(fi.Size()) > p.F("gb")*1e9), nil
		}})
	register(&NodeDef{Type: "check_audio", Label: "Audio codecs all in…", Category: "check",
		Help: "Yes if every audio track uses one of these codecs.", Outputs: []string{"Yes", "No"},
		Params: []Param{{Key: "codecs", Label: "Codecs", Kind: "list", Default: "aac,ac3,eac3"}},
		Run: func(j *Job, p Params) (int, error) {
			ok := p.List("codecs")
			for _, s := range j.Probe.Streams {
				if s.CodecType == "audio" && !slices.Contains(ok, s.CodecName) {
					return 2, nil
				}
			}
			return 1, nil
		}})
	register(&NodeDef{Type: "check_name", Label: "File name contains…", Category: "check",
		Help: "Case-insensitive. Example: remux", Outputs: []string{"Yes", "No"},
		Params: []Param{{Key: "text", Label: "Text", Kind: "text", Default: "remux"}},
		Run: func(j *Job, p Params) (int, error) {
			return yes(strings.Contains(strings.ToLower(filepath.Base(j.Path)), strings.ToLower(p.S("text")))), nil
		}})
	register(&NodeDef{Type: "check_saved", Label: "Saved at least…", Category: "check",
		Help: "Use after Run FFmpeg: compares the new file with the original.", Outputs: []string{"Yes", "No"},
		Params: []Param{{Key: "percent", Label: "Percent", Kind: "number", Default: 10.0}},
		Run: func(j *Job, p Params) (int, error) {
			if j.Cur == j.Path {
				return 0, fmt.Errorf("place this block after Run FFmpeg")
			}
			fi, err := os.Stat(j.Cur)
			if err != nil {
				return 0, err
			}
			saved := 100 * (1 - float64(fi.Size())/float64(j.OrigSize))
			j.logf("saved %.1f%%", saved)
			return yes(saved >= p.F("percent")), nil
		}})

	// --- video ---
	register(&NodeDef{Type: "video_encode", Label: "Video: Encode", Category: "video",
		Help:    "Re-encodes the video. \"Auto quality\" test-encodes short samples and picks the smallest size that still scores the VMAF target (95 ≈ visually identical).",
		Outputs: []string{"Next"},
		Params: []Param{
			{Key: "codec", Label: "Codec", Kind: "select", Options: []string{"hevc", "av1", "h264"}, Default: "hevc"},
			{Key: "mode", Label: "Quality", Kind: "select", Options: []string{"auto", "fixed"}, Default: "auto"},
			{Key: "vmaf", Label: "VMAF target (auto)", Kind: "number", Default: 95.0},
			{Key: "quality", Label: "Quality number (fixed, lower = better)", Kind: "number", Default: 24.0},
			{Key: "speed", Label: "Speed", Kind: "select", Options: []string{"fast", "balanced", "slow"}, Default: "balanced"},
			{Key: "hardware", Label: "Hardware", Kind: "select", Options: []string{"settings", "nvenc", "qsv", "vaapi", "cpu"}, Default: "settings",
				Help: "\"settings\" uses the choice on the Settings page."},
		},
		Run: func(j *Job, p Params) (int, error) {
			v := &j.Cmd.Video
			v.Encode, v.Codec, v.Speed = true, p.S("codec"), p.S("speed")
			v.Quality = int(p.F("quality"))
			v.VMAF = 0
			if p.S("mode") == "auto" {
				v.VMAF = p.F("vmaf")
			}
			v.Hardware = p.S("hardware")
			if v.Hardware == "settings" {
				v.Hardware = cfg().Hardware
			}
			return 1, nil
		}})
	register(&NodeDef{Type: "video_downscale", Label: "Video: Downscale", Category: "video",
		Help: "Shrinks video taller than this. Put it after Video: Encode.", Outputs: []string{"Next"},
		Params: []Param{{Key: "height", Label: "Max height", Kind: "select", Options: []string{"1440", "1080", "720"}, Default: "1080"}},
		Run:    func(j *Job, p Params) (int, error) { j.Cmd.Video.MaxHeight = int(p.F("height")); return 1, nil }})
	register(&NodeDef{Type: "strip_dv", Label: "Video: Remove Dolby Vision", Category: "video",
		Help: "Keeps the HDR10 base layer. For TVs that don't support Dolby Vision. No re-encode needed.", Outputs: []string{"Next"},
		Run: func(j *Job, p Params) (int, error) { j.Cmd.Video.StripDV = true; return 1, nil }})
	register(&NodeDef{Type: "set_container", Label: "Set container", Category: "video",
		Help: "MP4 plays on almost everything (Apple TV, browsers). MKV keeps every track type.", Outputs: []string{"Next"},
		Params: []Param{{Key: "container", Label: "Container", Kind: "select", Options: []string{"mp4", "mkv"}, Default: "mp4"}},
		Run:    func(j *Job, p Params) (int, error) { j.Cmd.Container = p.S("container"); return 1, nil }})

	// --- audio ---
	register(&NodeDef{Type: "audio_convert", Label: "Audio: Convert", Category: "audio",
		Help: "Tracks not in \"Keep codecs\" are converted. Leave Keep codecs empty to only convert what the container can't hold.", Outputs: []string{"Next"},
		Params: []Param{
			{Key: "keep", Label: "Keep codecs", Kind: "list", Default: "aac,ac3,eac3"},
			{Key: "to", Label: "Convert to", Kind: "select", Options: []string{"eac3", "ac3", "aac", "opus"}, Default: "eac3"},
			{Key: "bitrate", Label: "Bitrate", Kind: "select", Options: []string{"128k", "192k", "256k", "384k", "640k"}, Default: "640k"},
			{Key: "channels", Label: "Max channels", Kind: "select", Options: []string{"2", "6", "8"}, Default: "6"},
		},
		Run: func(j *Job, p Params) (int, error) {
			a := &j.Cmd.Audio
			a.KeepCodecs, a.ConvertTo, a.Bitrate = p.List("keep"), p.S("to"), p.S("bitrate")
			if a.KeepCodecs == nil {
				a.KeepCodecs = []string{} // empty, not nil: means "only what the container needs"
			}
			a.MaxChannels = int(p.F("channels"))
			return 1, nil
		}})
	register(&NodeDef{Type: "audio_langs", Label: "Audio: Keep languages", Category: "audio",
		Help: "3-letter codes, e.g. eng,jpn. Untagged tracks are always kept. If no track matches, all are kept.", Outputs: []string{"Next"},
		Params: []Param{{Key: "langs", Label: "Languages", Kind: "list", Default: "eng"}},
		Run:    func(j *Job, p Params) (int, error) { j.Cmd.Audio.Langs = p.List("langs"); return 1, nil }})
	register(&NodeDef{Type: "audio_stereo", Label: "Audio: Add stereo track", Category: "audio",
		Help: "Adds an AAC stereo copy of the first audio track when there is no stereo track. Helps phones, browsers and older TVs.", Outputs: []string{"Next"},
		Run: func(j *Job, p Params) (int, error) { j.Cmd.Audio.AddStereo = true; return 1, nil }})

	// --- subtitles ---
	register(&NodeDef{Type: "subs_langs", Label: "Subtitles: Keep languages", Category: "subtitles",
		Help: "3-letter codes, e.g. eng. Untagged subtitles are kept.", Outputs: []string{"Next"},
		Params: []Param{{Key: "langs", Label: "Languages", Kind: "list", Default: "eng"}},
		Run:    func(j *Job, p Params) (int, error) { j.Cmd.Subs.Langs = p.List("langs"); return 1, nil }})
	register(&NodeDef{Type: "subs_remove", Label: "Subtitles: Remove all", Category: "subtitles",
		Outputs: []string{"Next"},
		Run:     func(j *Job, p Params) (int, error) { j.Cmd.Subs.DropAll = true; return 1, nil }})

	// --- run ---
	register(&NodeDef{Type: "run_ffmpeg", Label: "Run FFmpeg", Category: "run",
		Help:    "Does all the work the blocks before it asked for, into a new temporary file. The original is untouched until Replace original.",
		Outputs: []string{"Done", "Nothing to do"},
		Run:     runFFmpegNode})

	// --- finish ---
	register(&NodeDef{Type: "replace", Label: "Replace original", Category: "finish",
		Help: "Swaps the new file in. \"move\" keeps the original in a folder you choose instead of deleting it.", Outputs: []string{"Next"},
		Params: []Param{
			{Key: "original", Label: "Original file", Kind: "select", Options: []string{"delete", "move"}, Default: "delete"},
			{Key: "folder", Label: "Move originals to", Kind: "text", Default: "/config/originals"},
		},
		Run: replaceNode})
	register(&NodeDef{Type: "keep_original", Label: "Keep original", Category: "finish",
		Help: "Ends here and throws away any new file. The original stays as it was.", Outputs: []string{},
		Params: []Param{{Key: "reason", Label: "Reason shown in history", Kind: "text", Default: "not worth it"}},
		Run: func(j *Job, p Params) (int, error) {
			j.Status, j.Detail = "skipped", p.S("reason")
			return 0, nil
		}})
	register(&NodeDef{Type: "notify_arr", Label: "Tell Radarr/Sonarr", Category: "finish",
		Help: "Asks Radarr/Sonarr (set up on the Settings page) to rescan this movie or show.", Outputs: []string{"Next"},
		Run: func(j *Job, p Params) (int, error) {
			if j.TestMode {
				j.logf("test mode: would ask Radarr/Sonarr to rescan")
				return 1, nil
			}
			path := j.Path
			if j.Final != "" {
				path = j.Final
			}
			j.logf("%s", notifyArrs(path))
			return 1, nil
		}})
	register(&NodeDef{Type: "notify_discord", Label: "Send Discord message", Category: "finish",
		Help: "Posts to the Discord webhook from the Settings page.", Outputs: []string{"Next"},
		Run: func(j *Job, p Params) (int, error) {
			if j.TestMode {
				j.logf("test mode: would send Discord message: %s", j.summary())
				return 1, nil
			}
			if err := discord(j.summary()); err != nil {
				j.logf("discord: %v", err)
			}
			return 1, nil
		}})
}

func (j *Job) summary() string {
	name := filepath.Base(j.Path)
	if !j.Replaced {
		return fmt.Sprintf("Shrinkarr: %s — %s", name, j.Detail)
	}
	fi, err := os.Stat(j.Final)
	if err != nil {
		return "Shrinkarr: replaced " + name
	}
	return fmt.Sprintf("Shrinkarr: %s %s → %s (saved %s)", name, human(j.OrigSize), human(fi.Size()), human(j.OrigSize-fi.Size()))
}

func human(b int64) string {
	f, unit := float64(b), "B"
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		if f < 1000 && f > -1000 {
			break
		}
		f /= 1000
		unit = u
	}
	return fmt.Sprintf("%.1f %s", f, unit)
}
