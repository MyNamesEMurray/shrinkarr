package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func st(i int, typ, codec string) Stream { return Stream{Index: i, CodecType: typ, CodecName: codec} }

func TestPlan(t *testing.T) {
	mk := func(format string, s ...Stream) Probe {
		var p Probe
		p.Format.FormatName = format
		p.Streams = s
		return p
	}
	if pl := makePlan(mk("mov,mp4,m4a,3gp,3g2,mj2", st(0, "video", "hevc"), st(1, "audio", "eac3"))); pl.Skip == "" {
		t.Fatal("HEVC MP4 with eac3 should skip")
	}
	if pl := makePlan(mk("matroska,webm", st(0, "video", "hevc"), st(1, "audio", "ac3"))); pl.Skip != "" || pl.Encode {
		t.Fatalf("HEVC MKV should remux, got %+v", pl)
	}
	if pl := makePlan(mk("matroska,webm", st(0, "video", "h264"))); !pl.Encode {
		t.Fatal("h264 should encode")
	}

	// Data/attachment streams with no codec_name (the Tdarr ffprobe bug) and PGS get dropped.
	truehd := st(2, "audio", "truehd")
	truehd.Channels = 8
	p := mk("matroska,webm", st(0, "attachment", ""), st(1, "video", "hevc"), truehd,
		st(3, "subtitle", "hdmv_pgs_subtitle"), st(4, "subtitle", "subrip"), st(5, "data", ""))
	p.Streams[1].SideData = append(p.Streams[1].SideData, struct {
		Type string `json:"side_data_type"`
	}{"DOVI configuration record"})
	pl := makePlan(p)
	got := strings.Join(append(pl.Args, videoArgs(pl, 0)...), " ")
	want := "-map 0:1 -map 0:2 -c:1 eac3 -b:1 640k -ac:1 6 -map 0:4 -c:2 mov_text -c:v copy -tag:v hvc1 -bsf:v dovi_rpu=strip=1"
	if got != want {
		t.Fatalf("args\n got %s\nwant %s", got, want)
	}

	cover := st(0, "video", "mjpeg")
	cover.Disposition.AttachedPic = 1
	if pl := makePlan(mk("matroska", cover, st(1, "video", "h264"))); pl.Video != 1 {
		t.Fatalf("cover art picked as video: %+v", pl)
	}
}

func TestFindCQ(t *testing.T) {
	cfg.CQMin, cfg.CQMax, cfg.VMAFFloor = 18, 32, 95
	// Fake: score falls 1 point per CQ step above 18.
	cq, s, _ := findCQ(func(cq int) (float64, error) { return 100 - float64(cq-18), nil })
	if cq != 23 || s != 95 {
		t.Fatalf("got cq %d score %v, want 23/95", cq, s)
	}
}

// End-to-end remux path with real ffmpeg: HEVC+FLAC+SRT MKV -> MP4 with eac3 and mov_text.
func TestRemuxE2E(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	cfg.TempDir = dir
	srt := filepath.Join(dir, "s.srt")
	os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:01,000\nhi\n"), 0o644)
	src := filepath.Join(dir, "movie.mkv")
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=d=3:s=320x240",
		"-f", "lavfi", "-i", "sine=d=3", "-i", srt, "-map", "0", "-map", "1", "-map", "2",
		"-c:v", "libx265", "-x265-params", "log-level=none", "-c:a", "flac", "-c:s", "srt", src).CombinedOutput()
	if err != nil {
		t.Skipf("cannot make fixture: %s", out)
	}
	status, detail, final := process(src)
	if status != "done" {
		t.Fatalf("%s: %s", status, detail)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("original mkv not removed")
	}
	p, err := ffprobe(final)
	if err != nil {
		t.Fatal(err)
	}
	var codecs []string
	for _, s := range p.Streams {
		codecs = append(codecs, s.CodecName)
	}
	if strings.Join(codecs, ",") != "hevc,eac3,mov_text" {
		t.Fatalf("streams %v", codecs)
	}
	tag, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_tag_string", "-of", "csv=p=0", final).Output()
	if strings.TrimSpace(string(tag)) != "hvc1" {
		t.Fatalf("tag %q, Apple TV needs hvc1", tag)
	}
	if pl := makePlan(p); pl.Skip == "" {
		t.Fatal("output should be skipped on next scan")
	}
}
