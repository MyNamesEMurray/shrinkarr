package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Stream struct {
	Index         int    `json:"index"`
	CodecType     string `json:"codec_type"`
	CodecName     string `json:"codec_name"`
	CodecTag      string `json:"codec_tag_string"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	PixFmt        string `json:"pix_fmt"`
	ColorTransfer string `json:"color_transfer"`
	Channels      int    `json:"channels"`
	BitRate       string `json:"bit_rate"`
	Tags          struct {
		Language string `json:"language"`
	} `json:"tags"`
	Disposition struct {
		Default     int `json:"default"`
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
	SideData []struct {
		Type string `json:"side_data_type"`
	} `json:"side_data_list"`
}

type Probe struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []Stream `json:"streams"`
}

func ffprobe(ctx context.Context, path string) (Probe, error) {
	var p Probe
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", path).Output()
	if err != nil {
		return p, fmt.Errorf("ffprobe failed on %s: %w", path, err)
	}
	return p, json.Unmarshal(out, &p)
}

func (p Probe) Duration() float64 {
	d, _ := strconv.ParseFloat(p.Format.Duration, 64)
	return d
}

// Video returns the main video stream (cover art skipped), or nil.
func (p Probe) Video() *Stream {
	for i := range p.Streams {
		s := &p.Streams[i]
		if s.CodecType == "video" && s.Disposition.AttachedPic == 0 {
			return s
		}
	}
	return nil
}

// Container returns "mp4", "mkv" or the raw ffprobe format name.
func (p Probe) Container() string {
	f := p.Format.FormatName
	switch {
	case strings.Contains(f, "mp4"):
		return "mp4"
	case strings.Contains(f, "matroska"):
		return "mkv"
	}
	return f
}

func (s *Stream) DV() bool {
	for _, sd := range s.SideData {
		if strings.Contains(sd.Type, "DOVI") {
			return true
		}
	}
	return false
}

func (s *Stream) HDR() bool {
	return s.ColorTransfer == "smpte2084" || s.ColorTransfer == "arib-std-b67" || s.DV()
}

func (s *Stream) TenBit() bool {
	return strings.Contains(s.PixFmt, "10") || strings.Contains(s.PixFmt, "12")
}

// Mbps returns the video bitrate, falling back to the whole file's bitrate.
func (p Probe) Mbps() float64 {
	v := p.Video()
	if v != nil {
		if b, err := strconv.ParseFloat(v.BitRate, 64); err == nil && b > 0 {
			return b / 1e6
		}
	}
	b, _ := strconv.ParseFloat(p.Format.BitRate, 64)
	return b / 1e6
}

// effHeight treats widescreen crops by their width: 3840x1600 counts as 2160p.
func (s *Stream) effHeight() int {
	return max(s.Height, s.Width*9/16)
}

// fitBox returns even dimensions that fit inside a 16:9 box of height h, or 0,0 if already small enough.
func (s *Stream) fitBox(h int) (int, int) {
	if h <= 0 || s.Width == 0 || s.effHeight() <= h {
		return 0, 0
	}
	f := min(float64(h*16/9)/float64(s.Width), float64(h)/float64(s.Height))
	even := func(x float64) int { return int(x/2+0.5) * 2 }
	return even(float64(s.Width) * f), even(float64(s.Height) * f)
}
