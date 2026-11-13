package main

import (
	"fmt"
	"strings"
)

type Stream struct {
	Index       int    `json:"index"`
	CodecType   string `json:"codec_type"`
	CodecName   string `json:"codec_name"`
	Channels    int    `json:"channels"`
	Disposition struct {
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
	} `json:"format"`
	Streams []Stream `json:"streams"`
}

// Plan describes what to do with one file. Skip != "" means leave it alone.
type Plan struct {
	Skip   string
	Video  int      // input stream index of the main video
	Encode bool     // true: re-encode video with NVENC; false: copy video
	DV     bool     // Dolby Vision RPU present, strip it
	Args   []string // -map and per-output-stream codec args, video codec excluded
}

// Codecs that direct play from MP4 on Apple TV.
var mp4Audio = map[string]bool{"aac": true, "ac3": true, "eac3": true, "alac": true, "mp3": true}
var textSubs = map[string]bool{"subrip": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true}

func hasDV(s Stream) bool {
	for _, sd := range s.SideData {
		if strings.Contains(sd.Type, "DOVI") {
			return true
		}
	}
	return false
}

func makePlan(p Probe) Plan {
	var v *Stream
	for i := range p.Streams {
		s := &p.Streams[i]
		if s.CodecType == "video" && s.Disposition.AttachedPic == 0 {
			v = s
			break
		}
	}
	if v == nil {
		return Plan{Skip: "no video stream"}
	}

	pl := Plan{Video: v.Index, Encode: v.CodecName != "hevc", DV: hasDV(*v)}
	audioOK := true
	pl.Args = []string{"-map", fmt.Sprintf("0:%d", v.Index)}
	out := 1
	for _, s := range p.Streams {
		switch {
		case s.CodecType == "audio":
			pl.Args = append(pl.Args, "-map", fmt.Sprintf("0:%d", s.Index))
			if mp4Audio[s.CodecName] {
				pl.Args = append(pl.Args, fmt.Sprintf("-c:%d", out), "copy")
			} else {
				audioOK = false
				pl.Args = append(pl.Args, fmt.Sprintf("-c:%d", out), "eac3", fmt.Sprintf("-b:%d", out), "640k")
				if s.Channels > 6 {
					pl.Args = append(pl.Args, fmt.Sprintf("-ac:%d", out), "6")
				}
			}
			out++
		case s.CodecType == "subtitle" && textSubs[s.CodecName]:
			pl.Args = append(pl.Args, "-map", fmt.Sprintf("0:%d", s.Index), fmt.Sprintf("-c:%d", out), "mov_text")
			out++
		}
		// image subs (PGS/VobSub), data and attachment streams cannot go in MP4: dropped.
	}

	mp4 := strings.Contains(p.Format.FormatName, "mp4")
	if mp4 && !pl.Encode && !pl.DV && audioOK {
		return Plan{Skip: "already HEVC MP4"}
	}
	return pl
}

// videoArgs returns the video codec args for a given CQ (ignored when copying).
func videoArgs(pl Plan, cq int) []string {
	if !pl.Encode {
		a := []string{"-c:v", "copy", "-tag:v", "hvc1"}
		if pl.DV {
			a = append(a, "-bsf:v", "dovi_rpu=strip=1")
		}
		return a
	}
	return []string{"-c:v", "hevc_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr",
		"-cq", fmt.Sprint(cq), "-b:v", "0", "-tag:v", "hvc1"}
}
