package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func ffprobe(path string) (Probe, error) {
	var p Probe
	out, err := exec.Command("ffprobe", "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", path).Output()
	if err != nil {
		return p, fmt.Errorf("ffprobe: %w", err)
	}
	return p, json.Unmarshal(out, &p)
}

func duration(p Probe) float64 {
	d, _ := strconv.ParseFloat(p.Format.Duration, 64)
	return d
}

func run(args ...string) error {
	if cfg.DryRun {
		logf("dry-run: ffmpeg %s", strings.Join(args, " "))
		return nil
	}
	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", append([]string{"-hide_banner", "-nostdin", "-y"}, args...)...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, tail(stderr.String(), 400))
	}
	return nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

var hw = []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}

// encodeWithFallback retries with CPU decode when NVDEC can't decode the source (Hi10P, VC-1, ...).
func encodeWithFallback(pre, post []string) error {
	err := run(append(append(append([]string{}, hw...), pre...), post...)...)
	if err != nil {
		err = run(append(append([]string{}, pre...), post...)...)
	}
	return err
}

// sampleVMAF encodes short clips at a CQ and returns the worst clip's mean VMAF.
func sampleVMAF(src string, pl Plan, dur float64, cq int, tmp string) (float64, error) {
	clip := 10.0
	starts := []float64{dur * 0.25, dur * 0.5, dur * 0.75}
	if dur < 4*clip {
		starts, clip = []float64{0}, dur
	}
	worst := 100.0
	for i, ss := range starts {
		enc := filepath.Join(tmp, fmt.Sprintf("s%d.mkv", i))
		log := filepath.Join(tmp, fmt.Sprintf("s%d.json", i))
		t := []string{"-ss", fmt.Sprint(ss), "-t", fmt.Sprint(clip)}
		post := append([]string{"-i", src, "-map", fmt.Sprintf("0:%d", pl.Video)}, videoArgs(pl, cq)...)
		if err := encodeWithFallback(t, append(post, "-an", "-sn", "-dn", enc)); err != nil {
			return 0, err
		}
		graph := fmt.Sprintf("[0:v]setpts=PTS-STARTPTS[d];[1:%d]setpts=PTS-STARTPTS[r];[d][r]libvmaf=n_subsample=5:n_threads=%d:log_fmt=json:log_path=%s",
			pl.Video, cfg.VMAFThreads, log)
		if err := run(append(append([]string{"-i", enc}, t...), "-i", src, "-lavfi", graph, "-f", "null", "-")...); err != nil {
			return 0, err
		}
		if cfg.DryRun {
			return 100, nil
		}
		s, err := readVMAF(log)
		if err != nil {
			return 0, err
		}
		worst = math.Min(worst, s)
	}
	return worst, nil
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
		return 0, errors.New("no vmaf mean in log")
	}
	return *r.Pooled.VMAF.Mean, nil
}

// findCQ binary-searches the highest CQ (smallest file) whose samples meet the VMAF floor.
func findCQ(score func(int) (float64, error)) (int, float64, error) {
	lo, hi, best, bestScore := cfg.CQMin, cfg.CQMax, cfg.CQMin, 0.0
	for lo <= hi {
		mid := (lo + hi) / 2
		s, err := score(mid)
		if err != nil {
			return 0, 0, err
		}
		if s >= cfg.VMAFFloor {
			best, bestScore, lo = mid, s, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best, bestScore, nil
}

// process converts one file. Returns final path and a short status detail.
func process(src string) (status, detail, final string) {
	p, err := ffprobe(src)
	if err != nil {
		return "failed", err.Error(), ""
	}
	pl := makePlan(p)
	if pl.Skip != "" {
		return "skipped", pl.Skip, ""
	}
	dur := duration(p)

	tmp, err := os.MkdirTemp(cfg.TempDir, "job-")
	if err != nil {
		return "failed", err.Error(), ""
	}
	defer os.RemoveAll(tmp)

	cq, detail := 0, "remux"
	if pl.Encode {
		var score float64
		cq, score, err = findCQ(func(cq int) (float64, error) { return sampleVMAF(src, pl, dur, cq, tmp) })
		if err != nil {
			return "failed", err.Error(), ""
		}
		detail = fmt.Sprintf("cq %d, vmaf %.1f", cq, score)
	}
	if pl.DV {
		detail += ", DV stripped"
	}

	dir, base := filepath.Split(src)
	final = filepath.Join(dir, strings.TrimSuffix(base, filepath.Ext(base))+".mp4")
	if final != src {
		if _, err := os.Stat(final); err == nil {
			return "failed", "target exists: " + final, ""
		}
	}
	// Temp output lives next to the source so the final rename is atomic; dotfile so Plex/arrs ignore it.
	out := filepath.Join(dir, ".tinydarr-"+filepath.Base(final))
	args := append([]string{"-i", src}, pl.Args...)
	args = append(args, videoArgs(pl, cq)...)
	args = append(args, "-map_chapters", "0", "-f", "mp4", out)
	if pl.Encode {
		err = encodeWithFallback(nil, args)
	} else {
		err = run(args...)
	}
	if err != nil {
		os.Remove(out)
		return "failed", err.Error(), ""
	}
	if cfg.DryRun {
		return "skipped", "dry-run: " + detail, ""
	}

	if err := verify(src, out, p, pl.Encode); err != nil {
		os.Remove(out)
		return "failed", err.Error(), ""
	}
	if err := swap(src, out, final); err != nil {
		os.Remove(out)
		return "failed", err.Error(), ""
	}
	return "done", detail, final
}

func verify(src, out string, in Probe, encoded bool) error {
	o, err := ffprobe(out)
	if err != nil {
		return err
	}
	if math.Abs(duration(o)-duration(in)) > 2 {
		return fmt.Errorf("duration mismatch: in %.1fs out %.1fs", duration(in), duration(o))
	}
	if len(o.Streams) == 0 || o.Streams[0].CodecName != "hevc" {
		return errors.New("output has no hevc video")
	}
	si, _ := os.Stat(src)
	so, _ := os.Stat(out)
	if encoded && so.Size() >= si.Size() {
		return fmt.Errorf("no gain: %d >= %d bytes, kept original", so.Size(), si.Size())
	}
	return nil
}

// swap gives out the source's owner and mode, moves it into place, then removes the source.
func swap(src, out, final string) error {
	si, err := os.Stat(src)
	if err != nil {
		return err
	}
	if st, ok := si.Sys().(*syscall.Stat_t); ok {
		os.Chown(out, int(st.Uid), int(st.Gid))
	}
	os.Chmod(out, si.Mode().Perm())
	if err := os.Rename(out, final); err != nil {
		return err
	}
	if final != src {
		return os.Remove(src)
	}
	return nil
}
