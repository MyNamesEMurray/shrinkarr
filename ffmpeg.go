package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

var mp4Audio = []string{"aac", "ac3", "eac3", "alac", "mp3", "opus", "flac"}
var textSubs = []string{"subrip", "ass", "ssa", "mov_text", "webvtt", "text"}

type buildOpts struct {
	enc    *Encoder
	hw     bool
	q      int
	out    string
	sample bool
	ss, t  float64
}

func keepLangs(ss []Stream, langs []string) []Stream {
	if len(langs) == 0 {
		return ss
	}
	var out []Stream
	for _, s := range ss {
		l := strings.ToLower(s.Tags.Language)
		if l == "" || l == "und" || slices.Contains(langs, l) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return ss
	}
	return out
}

func outContainer(j *Job) string {
	if c := j.Cmd.Container; c != "" {
		return c
	}
	if c := j.Probe.Container(); c == "mp4" {
		return c
	}
	return "mkv"
}

// buildArgs turns the collected Cmd into ffmpeg args. changed=false means output would equal input.
func (j *Job) buildArgs(o buildOpts) (args []string, changed bool, err error) {
	p, c := j.Probe, j.Cmd
	v := p.Video()
	if v == nil {
		return nil, false, errors.New("file has no video stream")
	}
	cont := outContainer(j)
	changed = cont != p.Container()

	if c.Video.Encode {
		args = append(args, o.enc.inputArgs(o.hw)...)
	}
	if o.sample {
		args = append(args, "-ss", fmt.Sprint(o.ss), "-t", fmt.Sprint(o.t))
	}
	args = append(args, "-i", j.Cur, "-map", fmt.Sprintf("0:%d", v.Index))

	codec := v.CodecName
	w, h := v.fitBox(c.Video.MaxHeight)
	if c.Video.Encode {
		codec = c.Video.Codec
		changed = true
		args = append(args, "-c:v", o.enc.Names[codec])
		if vf := o.enc.filter(o.hw, w, h, codec, v.TenBit()); vf != "" {
			args = append(args, "-vf", vf)
		}
		args = append(args, o.enc.qualityArgs(codec, o.q, c.Video.Speed)...)
	} else {
		if w > 0 {
			return nil, false, errors.New("Video: Downscale needs a Video: Encode block before Run FFmpeg")
		}
		args = append(args, "-c:v", "copy")
	}
	if codec == "hevc" && cont == "mp4" {
		args = append(args, "-tag:v", "hvc1") // Apple devices refuse hev1
		if !c.Video.Encode && v.CodecTag != "hvc1" {
			changed = true
		}
	}
	if c.Video.StripDV && v.DV() && (codec == "hevc" || codec == "av1") {
		hwMu.RLock()
		ok := hasDoviBSF
		hwMu.RUnlock()
		if !ok {
			return nil, false, errors.New("this ffmpeg can't remove Dolby Vision (needs ffmpeg 7+)")
		}
		args = append(args, "-bsf:v", "dovi_rpu=strip=1")
		changed = true
	}
	if o.sample {
		return append(args, "-an", "-sn", "-dn", "-f", "matroska", o.out), changed, nil
	}

	n := 1 // output stream index
	var audio, subs []Stream
	hasAttach := false
	for _, s := range p.Streams {
		switch s.CodecType {
		case "audio":
			audio = append(audio, s)
		case "subtitle":
			subs = append(subs, s)
		case "attachment":
			hasAttach = true
		}
	}
	kept := keepLangs(audio, c.Audio.Langs)
	if len(kept) < len(audio) {
		changed = true
	}
	stereo := false
	for _, s := range kept {
		args = append(args, "-map", fmt.Sprintf("0:%d", s.Index))
		convert := cont == "mp4" && !slices.Contains(mp4Audio, s.CodecName)
		if len(c.Audio.KeepCodecs) > 0 && !slices.Contains(c.Audio.KeepCodecs, s.CodecName) {
			convert = true
		}
		ch := s.Channels
		if convert {
			to, br, maxCh := c.Audio.ConvertTo, c.Audio.Bitrate, c.Audio.MaxChannels
			if to == "" {
				to, br, maxCh = "eac3", "640k", 6
			}
			if (to == "eac3" || to == "ac3") && (maxCh == 0 || maxCh > 6) {
				maxCh = 6
			}
			args = append(args, fmt.Sprintf("-c:%d", n), to, fmt.Sprintf("-b:%d", n), br)
			if maxCh > 0 && ch > maxCh {
				args = append(args, fmt.Sprintf("-ac:%d", n), fmt.Sprint(maxCh))
				ch = maxCh
			}
			changed = true
		} else {
			args = append(args, fmt.Sprintf("-c:%d", n), "copy")
		}
		stereo = stereo || (ch > 0 && ch <= 2)
		n++
	}
	if c.Audio.AddStereo && !stereo && len(kept) > 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", kept[0].Index), fmt.Sprintf("-c:%d", n), "aac",
			fmt.Sprintf("-ac:%d", n), "2", fmt.Sprintf("-b:%d", n), "192k", fmt.Sprintf("-metadata:s:%d", n), "title=Stereo",
			fmt.Sprintf("-disposition:%d", n), "0")
		n++
		changed = true
	}

	keptSubs := keepLangs(subs, c.Subs.Langs)
	if c.Subs.DropAll {
		keptSubs = nil
	}
	if len(keptSubs) < len(subs) {
		changed = true
	}
	for _, s := range keptSubs {
		switch {
		case cont == "mp4" && slices.Contains(textSubs, s.CodecName):
			args = append(args, "-map", fmt.Sprintf("0:%d", s.Index), fmt.Sprintf("-c:%d", n), "mov_text")
			changed = changed || s.CodecName != "mov_text"
		case cont == "mp4":
			changed = true // image subs (PGS/VobSub) can't go in MP4
			continue
		case s.CodecName == "mov_text":
			args = append(args, "-map", fmt.Sprintf("0:%d", s.Index), fmt.Sprintf("-c:%d", n), "srt")
		default:
			args = append(args, "-map", fmt.Sprintf("0:%d", s.Index), fmt.Sprintf("-c:%d", n), "copy")
		}
		n++
	}
	if cont == "mkv" && hasAttach {
		args = append(args, "-map", "0:t?", "-c:t", "copy") // fonts for styled subtitles
	}
	format := map[string]string{"mp4": "mp4", "mkv": "matroska"}[cont]
	args = append(args, "-map_metadata", "0", "-map_chapters", "0", "-max_muxing_queue_size", "4096", "-f", format, o.out)
	return args, changed, nil
}

func shellQuote(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " '\"[];|&$()") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		q[i] = a
	}
	return strings.Join(q, " ")
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// ffmpeg runs one command and reports progress against dur seconds.
func (j *Job) ffmpeg(phase string, dur float64, args []string) error {
	full := append([]string{"-hide_banner", "-nostdin", "-y", "-v", "error", "-progress", "pipe:1", "-nostats"}, args...)
	j.Commands = append(j.Commands, "ffmpeg "+shellQuote(full))
	if j.TestMode {
		return nil
	}
	j.setProgress(Progress{Phase: phase})
	cmd := exec.CommandContext(j.ctx, "ffmpeg", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	pr := Progress{Phase: phase}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "out_time_us":
			if us, err := strconv.ParseFloat(v, 64); err == nil && dur > 0 {
				pr.Percent = math.Min(100, us/1e6/dur*100)
			}
		case "fps":
			pr.FPS = v
		case "speed":
			pr.Speed = strings.TrimSpace(v)
		case "progress":
			j.setProgress(pr)
		}
	}
	if err := cmd.Wait(); err != nil {
		if j.ctx.Err() != nil {
			return errors.New("cancelled")
		}
		return fmt.Errorf("ffmpeg failed: %s", tail(stderr.String(), 600))
	}
	return nil
}

// encode runs a build, retrying with CPU decoding if GPU decoding fails (Hi10P H.264, VC-1, ...).
func (j *Job) encode(phase string, dur float64, o buildOpts) error {
	o.hw = j.Cmd.Video.Encode && o.enc != nil && o.enc.ID != "cpu" && !j.hwFailed
	args, _, err := j.buildArgs(o)
	if err != nil {
		return err
	}
	err = j.ffmpeg(phase, dur, args)
	if err != nil && o.hw && j.ctx.Err() == nil {
		j.logf("GPU decode failed, retrying with CPU decode: %v", err)
		j.hwFailed = true
		o.hw = false
		if args, _, err = j.buildArgs(o); err != nil {
			return err
		}
		err = j.ffmpeg(phase, dur, args)
	}
	return err
}

func readVMAF(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var r struct {
		Pooled struct {
			VMAF struct {
				Mean *float64 `json:"mean"`
			} `json:"vmaf"`
		} `json:"pooled_metrics"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return 0, err
	}
	if r.Pooled.VMAF.Mean == nil {
		return 0, errors.New("no VMAF score in log")
	}
	return *r.Pooled.VMAF.Mean, nil
}

// searchQuality binary-searches the highest quality number (smallest file) that still meets target.
func searchQuality(lo, hi int, target float64, score func(int) (float64, error)) (int, float64, error) {
	best, bestScore := lo, 0.0
	for lo <= hi {
		mid := (lo + hi) / 2
		s, err := score(mid)
		if err != nil {
			return 0, 0, err
		}
		if s >= target {
			best, bestScore, lo = mid, s, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best, bestScore, nil
}

func (j *Job) findQuality(enc *Encoder) (int, float64, error) {
	tmp, err := os.MkdirTemp(cfg().TempDir, "shrinkarr-")
	if err != nil {
		return 0, 0, err
	}
	defer os.RemoveAll(tmp)
	v := j.Probe.Video()
	dur := j.Probe.Duration()
	clip, starts := 10.0, []float64{dur * 0.25, dur * 0.5, dur * 0.75}
	if dur < 40 {
		clip, starts = dur, []float64{0}
	}
	threads := max(1, cfg().VMAFThreads)
	try := 0
	score := func(q int) (float64, error) {
		try++
		worst := 100.0
		for i, ss := range starts {
			sample := filepath.Join(tmp, fmt.Sprintf("s%d.mkv", i))
			logp := filepath.Join(tmp, fmt.Sprintf("s%d.json", i))
			phase := fmt.Sprintf("Finding quality: try %d, sample %d of %d", try, i+1, len(starts))
			if err := j.encode(phase, clip, buildOpts{enc: enc, q: q, out: sample, sample: true, ss: ss, t: clip}); err != nil {
				return 0, err
			}
			graph := fmt.Sprintf("[0:v]scale=%d:%d:flags=bicubic,setpts=PTS-STARTPTS[d];[1:%d]setpts=PTS-STARTPTS[r];[d][r]libvmaf=n_subsample=5:n_threads=%d:log_fmt=json:log_path=%s",
				v.Width, v.Height, v.Index, threads, logp)
			args := []string{"-i", sample, "-ss", fmt.Sprint(ss), "-t", fmt.Sprint(clip), "-i", j.Cur, "-lavfi", graph, "-f", "null", "-"}
			if err := j.ffmpeg(phase+" (measuring)", clip, args); err != nil {
				return 0, err
			}
			s, err := readVMAF(logp)
			if err != nil {
				return 0, err
			}
			worst = math.Min(worst, s)
		}
		j.logf("quality %d: VMAF %.2f", q, worst)
		return worst, nil
	}
	return searchQuality(18, 34, j.Cmd.Video.VMAF, score)
}

func runFFmpegNode(j *Job, p Params) (int, error) {
	c := j.Cmd
	var enc *Encoder
	if c.Video.Encode {
		e, err := pickEncoder(c.Video.Codec, c.Video.Hardware)
		if err != nil {
			return 0, err
		}
		enc, j.enc = e, e
	}
	if _, changed, err := j.buildArgs(buildOpts{enc: enc, q: c.Video.Quality}); err != nil {
		return 0, err
	} else if !changed {
		j.Cmd = Cmd{}
		return 2, nil
	}

	q := c.Video.Quality
	if c.Video.Encode {
		hwMu.RLock()
		vmafOK := hasVMAF
		hwMu.RUnlock()
		switch {
		case c.Video.VMAF <= 0:
		case j.TestMode:
			j.logf("test mode: would search for the smallest size scoring VMAF %.0f", c.Video.VMAF)
		case !vmafOK:
			j.logf("libvmaf missing from ffmpeg; using fixed quality %d", q)
		default:
			var s float64
			var err error
			if q, s, err = j.findQuality(enc); err != nil {
				return 0, err
			}
			j.notes = append(j.notes, fmt.Sprintf("VMAF %.1f", s))
		}
		j.notes = append(j.notes, fmt.Sprintf("%s q%d", enc.Names[c.Video.Codec], q))
	} else {
		j.notes = append(j.notes, "remux")
	}
	if c.Video.StripDV {
		if v := j.Probe.Video(); v != nil && v.DV() {
			j.notes = append(j.notes, "Dolby Vision removed")
		}
	}

	ext := map[string]string{"mp4": ".mp4", "mkv": ".mkv"}[outContainer(j)]
	// Hidden temp file beside the original: the final rename stays on one disk, and Plex/arrs ignore dotfiles.
	out := filepath.Join(filepath.Dir(j.Path), fmt.Sprintf(".shrinkarr-%s-%d%s", j.ID, len(j.temps), ext))
	j.temps = append(j.temps, out)
	if err := j.encode("Converting", j.Probe.Duration(), buildOpts{enc: enc, q: q, out: out}); err != nil {
		return 0, err
	}
	if j.TestMode {
		j.Status, j.Detail = "skipped", "Test mode, nothing changed. Plan: "+strings.Join(j.notes, ", ")
		return 0, nil
	}
	np, err := ffprobe(j.ctx, out)
	if err != nil {
		return 0, err
	}
	if d := math.Abs(np.Duration() - j.Probe.Duration()); d > math.Max(2, j.Probe.Duration()*0.01) {
		return 0, fmt.Errorf("new file is %.0fs long but the original is %.0fs; kept the original", np.Duration(), j.Probe.Duration())
	}
	if np.Video() == nil {
		return 0, errors.New("new file has no video; kept the original")
	}
	j.Cur, j.Probe, j.Cmd = out, np, Cmd{}
	return 1, nil
}

func replaceNode(j *Job, p Params) (int, error) {
	if j.Cur == j.Path {
		j.logf("nothing to replace: no new file yet")
		return 1, nil
	}
	final := strings.TrimSuffix(j.Path, filepath.Ext(j.Path)) + filepath.Ext(j.Cur)
	if final != j.Path {
		if _, err := os.Stat(final); err == nil {
			return 0, fmt.Errorf("%s already exists; not overwriting it", filepath.Base(final))
		}
	}
	si, err := os.Stat(j.Path)
	if err != nil {
		return 0, err
	}
	if st, ok := si.Sys().(*syscall.Stat_t); ok {
		os.Chown(j.Cur, int(st.Uid), int(st.Gid)) // keep Unraid's nobody:users
	}
	os.Chmod(j.Cur, si.Mode().Perm())

	if p.S("original") == "move" {
		rel, err := filepath.Rel(j.Lib.Path, j.Path)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(j.Path)
		}
		folder := p.S("folder")
		if folder == "" {
			folder = filepath.Join(j.Lib.Path, ".shrinkarr-originals")
		}
		dest := filepath.Join(folder, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return 0, err
		}
		if err := moveFile(j.Path, dest); err != nil {
			return 0, fmt.Errorf("moving original: %w", err)
		}
		if err := os.Rename(j.Cur, final); err != nil {
			if back := moveFile(dest, j.Path); back != nil {
				return 0, fmt.Errorf("%w; the original is still at %s", err, dest)
			}
			return 0, err
		}
		j.logf("original moved to %s", dest)
	} else if err := os.Rename(j.Cur, final); err != nil {
		return 0, err
	}
	j.temps = slices.DeleteFunc(j.temps, func(t string) bool { return t == j.Cur })
	j.Final, j.Cur, j.Replaced = final, final, true
	// The new file is in place now; failing to delete the old one must not mark the job failed.
	if final != j.Path && p.S("original") != "move" {
		if err := os.Remove(j.Path); err != nil {
			j.logf("could not delete the original %s: %v", j.Path, err)
			j.notes = append(j.notes, "original not deleted")
		}
	}
	return 1, nil
}

// moveFile renames, or copies then deletes when the destination is on another disk.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}
