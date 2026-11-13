package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Encoder describes one family of hardware (or CPU) encoders and how to drive it.
type Encoder struct {
	ID    string            `json:"id"`
	Label string            `json:"label"`
	Names map[string]string `json:"-"` // target codec -> ffmpeg encoder
	// Works lists target codecs that passed the startup self-test.
	Works []string `json:"works"`
}

var encoders = []*Encoder{
	{ID: "nvenc", Label: "NVIDIA (NVENC)", Names: map[string]string{"hevc": "hevc_nvenc", "av1": "av1_nvenc", "h264": "h264_nvenc"}},
	{ID: "qsv", Label: "Intel QuickSync", Names: map[string]string{"hevc": "hevc_qsv", "av1": "av1_qsv", "h264": "h264_qsv"}},
	{ID: "vaapi", Label: "AMD / Intel (VAAPI)", Names: map[string]string{"hevc": "hevc_vaapi", "av1": "av1_vaapi", "h264": "h264_vaapi"}},
	{ID: "cpu", Label: "CPU (software)", Names: map[string]string{"hevc": "libx265", "av1": "libsvtav1", "h264": "libx264"}},
}

var (
	hwMu       sync.RWMutex
	hasVMAF    bool
	hasDoviBSF bool
	ffmpegVer  string
	detecting  = true
)

// detectHardware test-encodes a few frames with every encoder so "auto" only picks ones that really work.
func detectHardware() {
	hwMu.Lock()
	detecting = true
	hwMu.Unlock()
	out, _ := exec.Command("ffmpeg", "-hide_banner", "-version").Output()
	ver := strings.SplitN(string(out), "\n", 2)[0]
	filters, _ := exec.Command("ffmpeg", "-hide_banner", "-filters").Output()
	bsfs, _ := exec.Command("ffmpeg", "-hide_banner", "-bsfs").Output()
	dev := cfg().VAAPIDevice
	for _, e := range encoders {
		works := []string{}
		for _, codec := range []string{"hevc", "av1", "h264"} {
			args := []string{"-hide_banner", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x240:d=0.2"}
			if e.ID == "vaapi" {
				args = append(args, "-vaapi_device", dev, "-vf", "format=nv12,hwupload")
			}
			args = append(args, "-frames:v", "3", "-c:v", e.Names[codec], "-f", "null", "-")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if exec.CommandContext(ctx, "ffmpeg", args...).Run() == nil {
				works = append(works, codec)
			}
			cancel()
		}
		hwMu.Lock()
		e.Works = works
		hwMu.Unlock()
		log.Printf("encoder %s: %v", e.ID, works)
	}
	hwMu.Lock()
	ffmpegVer = ver
	hasVMAF = strings.Contains(string(filters), "libvmaf")
	hasDoviBSF = strings.Contains(string(bsfs), "dovi_rpu")
	detecting = false
	hwMu.Unlock()
}

func (e *Encoder) supports(codec string) bool {
	hwMu.RLock()
	defer hwMu.RUnlock()
	for _, c := range e.Works {
		if c == codec {
			return true
		}
	}
	return false
}

// pickEncoder returns the encoder to use for a codec. pref is a hardware ID or "auto".
func pickEncoder(codec, pref string) (*Encoder, error) {
	for _, e := range encoders {
		if (pref == "auto" || pref == "" || pref == e.ID) && e.supports(codec) {
			return e, nil
		}
	}
	if pref != "auto" && pref != "" {
		return nil, fmt.Errorf("%s cannot encode %s on this machine (check the Settings page for detected hardware)", pref, codec)
	}
	return nil, fmt.Errorf("no working %s encoder found", codec)
}

// inputArgs: decoder args placed before -i. hw=true decodes on the GPU and keeps frames there.
func (e *Encoder) inputArgs(hw bool) []string {
	dev := cfg().VAAPIDevice
	switch {
	case e.ID == "nvenc" && hw:
		return []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
	case e.ID == "qsv" && hw:
		return []string{"-hwaccel", "qsv", "-hwaccel_output_format", "qsv"}
	case e.ID == "vaapi" && hw:
		return []string{"-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi", "-vaapi_device", dev}
	case e.ID == "vaapi":
		return []string{"-vaapi_device", dev}
	}
	return nil
}

// filter builds the -vf chain. w,h 0 = no scaling.
func (e *Encoder) filter(hw bool, w, h int, codec string, tenBit bool) string {
	var f []string
	if hw {
		if w > 0 {
			switch e.ID {
			case "nvenc":
				f = append(f, fmt.Sprintf("scale_cuda=%d:%d", w, h))
			case "qsv":
				f = append(f, fmt.Sprintf("scale_qsv=w=%d:h=%d", w, h))
			case "vaapi":
				f = append(f, fmt.Sprintf("scale_vaapi=w=%d:h=%d", w, h))
			}
		}
		return strings.Join(f, ",")
	}
	if w > 0 {
		f = append(f, fmt.Sprintf("scale=%d:%d", w, h))
	}
	eightBit := codec == "h264" || !tenBit
	if codec == "h264" {
		f = append(f, "format=yuv420p")
	}
	if e.ID == "vaapi" {
		if eightBit {
			f = append(f, "format=nv12", "hwupload")
		} else {
			f = append(f, "format=p010", "hwupload")
		}
	}
	return strings.Join(f, ",")
}

// qualityArgs maps one 0-51 style quality number onto each encoder's own knob.
func (e *Encoder) qualityArgs(codec string, q int, speed string) []string {
	qs := fmt.Sprint(q)
	if speed != "fast" && speed != "slow" {
		speed = "balanced"
	}
	switch e.ID {
	case "nvenc":
		p := map[string]string{"fast": "p3", "balanced": "p5", "slow": "p7"}[speed]
		return []string{"-preset", p, "-tune", "hq", "-rc", "vbr", "-cq", qs, "-b:v", "0"}
	case "qsv":
		p := map[string]string{"fast": "faster", "balanced": "medium", "slow": "slower"}[speed]
		return []string{"-preset", p, "-global_quality", qs}
	case "vaapi":
		return []string{"-rc_mode", "CQP", "-qp", qs}
	}
	switch codec {
	case "av1":
		p := map[string]string{"fast": "10", "balanced": "8", "slow": "5"}[speed]
		return []string{"-preset", p, "-crf", qs}
	case "hevc":
		p := map[string]string{"fast": "fast", "balanced": "medium", "slow": "slow"}[speed]
		return []string{"-preset", p, "-crf", qs, "-x265-params", "log-level=error"}
	}
	p := map[string]string{"fast": "fast", "balanced": "medium", "slow": "slow"}[speed]
	return []string{"-preset", p, "-crf", qs}
}
