package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Flow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	BuiltIn     bool   `json:"builtIn,omitempty"`
	Nodes       []Node `json:"nodes"`
	Edges       []Edge `json:"edges"`
}

type Node struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	X      float64        `json:"x,omitempty"`
	Y      float64        `json:"y,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

type Edge struct {
	From string `json:"from"`
	Out  int    `json:"out"` // 1-based output port
	To   string `json:"to"`
}

type Params map[string]any

func (p Params) S(k string) string {
	switch v := p[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func (p Params) F(k string) float64 {
	f, _ := strconv.ParseFloat(p.S(k), 64)
	return f
}

func (p Params) List(k string) []string {
	var out []string
	for _, s := range strings.Split(strings.ToLower(p.S(k)), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

type Param struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"` // select number text list
	Options []string `json:"options,omitempty"`
	Default any      `json:"default"`
	Help    string   `json:"help,omitempty"`
}

type NodeDef struct {
	Type     string   `json:"type"`
	Label    string   `json:"label"`
	Category string   `json:"category"` // start check action run finish
	Help     string   `json:"help"`
	Outputs  []string `json:"outputs"`
	Params   []Param  `json:"params"`
	// Run returns the 1-based output to follow, or 0 to end the flow.
	Run func(j *Job, p Params) (int, error) `json:"-"`
}

// Cmd collects what the action blocks asked for until a Run FFmpeg block executes it.
type Cmd struct {
	Container string
	Video     struct {
		Encode    bool
		Codec     string
		Hardware  string
		Quality   int
		VMAF      float64
		Speed     string
		MaxHeight int
		StripDV   bool
	}
	Audio struct {
		KeepCodecs  []string // nil = keep anything the container allows
		ConvertTo   string
		Bitrate     string
		MaxChannels int
		Langs       []string
		AddStereo   bool
	}
	Subs struct {
		Langs   []string
		DropAll bool
	}
}

type Progress struct {
	Phase   string  `json:"phase"`
	Percent float64 `json:"percent"`
	FPS     string  `json:"fps"`
	Speed   string  `json:"speed"`
}

type Job struct {
	ID       string
	Path     string
	Lib      Library
	Flow     *Flow
	ctx      context.Context
	cancel   context.CancelFunc
	TestMode bool

	Orig     Probe
	OrigSize int64
	Cur      string // file the next step reads: the original, or the last ffmpeg output
	Probe    Probe
	Cmd      Cmd
	temps    []string
	Final    string
	Replaced bool
	hwFailed bool // GPU decode failed once; use CPU decode for the rest of the job
	enc      *Encoder
	notes    []string

	Steps    []string
	Visited  []string
	Commands []string
	Status   string
	Detail   string
	log      strings.Builder

	progMu sync.Mutex
	prog   Progress
}

func (j *Job) logf(format string, a ...any) {
	j.progMu.Lock()
	fmt.Fprintf(&j.log, format+"\n", a...)
	j.progMu.Unlock()
}

func (j *Job) setProgress(p Progress) {
	j.progMu.Lock()
	j.prog = p
	j.progMu.Unlock()
}

func (j *Job) Progress() Progress {
	j.progMu.Lock()
	defer j.progMu.Unlock()
	return j.prog
}

var nodeDefs = map[string]*NodeDef{}

func register(d *NodeDef) { nodeDefs[d.Type] = d }

func nodeList() []*NodeDef {
	order := map[string]int{"start": 0, "check": 1, "video": 2, "audio": 3, "subtitles": 4, "run": 5, "finish": 6}
	var out []*NodeDef
	for _, d := range nodeDefs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, k int) bool {
		if order[out[i].Category] != order[out[k].Category] {
			return order[out[i].Category] < order[out[k].Category]
		}
		return out[i].Label < out[k].Label
	})
	return out
}

func withDefaults(d *NodeDef, raw map[string]any) Params {
	p := Params{}
	for _, prm := range d.Params {
		p[prm.Key] = prm.Default
	}
	for k, v := range raw {
		p[k] = v
	}
	return p
}

// validate catches broken flows before they touch any file.
func (f *Flow) validate() error {
	starts := 0
	ids := map[string]*Node{}
	for i := range f.Nodes {
		n := &f.Nodes[i]
		if nodeDefs[n.Type] == nil {
			return fmt.Errorf("unknown block type %q", n.Type)
		}
		if n.Type == "start" {
			starts++
		}
		ids[n.ID] = n
	}
	if starts != 1 {
		return errors.New("a flow needs exactly one Start block")
	}
	seen := map[string]bool{}
	for _, e := range f.Edges {
		if ids[e.From] == nil || ids[e.To] == nil {
			return errors.New("a connection points to a missing block")
		}
		if e.Out < 1 || e.Out > len(nodeDefs[ids[e.From].Type].Outputs) {
			return fmt.Errorf("bad output %d on %s", e.Out, nodeDefs[ids[e.From].Type].Label)
		}
		k := fmt.Sprintf("%s:%d", e.From, e.Out)
		if seen[k] {
			return fmt.Errorf("an output of %s is connected twice; each output can lead to one block", nodeDefs[ids[e.From].Type].Label)
		}
		seen[k] = true
	}
	return nil
}

func (j *Job) runFlow() error {
	f := j.Flow
	if err := f.validate(); err != nil {
		return err
	}
	byID := map[string]*Node{}
	var cur *Node
	for i := range f.Nodes {
		byID[f.Nodes[i].ID] = &f.Nodes[i]
		if f.Nodes[i].Type == "start" {
			cur = &f.Nodes[i]
		}
	}
	next := map[string]string{}
	for _, e := range f.Edges {
		next[fmt.Sprintf("%s:%d", e.From, e.Out)] = e.To
	}
	for steps := 0; cur != nil; steps++ {
		if steps > 200 {
			return errors.New("flow ran more than 200 blocks; it probably loops")
		}
		if err := j.ctx.Err(); err != nil {
			return errors.New("cancelled")
		}
		d := nodeDefs[cur.Type]
		out, err := d.Run(j, withDefaults(d, cur.Params))
		step := d.Label
		if out > 0 && len(d.Outputs) > 1 {
			step += " → " + d.Outputs[out-1]
		}
		j.Steps = append(j.Steps, step)
		j.Visited = append(j.Visited, cur.ID)
		j.logf("[%s] %s", cur.ID, step)
		if err != nil {
			return fmt.Errorf("%s: %w", d.Label, err)
		}
		if out == 0 || j.Status != "" {
			break
		}
		cur = byID[next[fmt.Sprintf("%s:%d", cur.ID, out)]]
	}
	return nil
}

// --- flow storage ---

var (
	flowMu       sync.RWMutex
	builtinFlows []*Flow
)

func flowsDir() string { return filepath.Join(configDir, "flows") }

func allFlows() []*Flow {
	flowMu.RLock()
	out := append([]*Flow(nil), builtinFlows...)
	flowMu.RUnlock()
	entries, _ := os.ReadDir(flowsDir())
	for _, e := range entries {
		if f, err := readFlow(filepath.Join(flowsDir(), e.Name())); err == nil {
			out = append(out, f)
		}
	}
	return out
}

func readFlow(path string) (*Flow, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Flow
	return &f, json.Unmarshal(b, &f)
}

func getFlow(id string) *Flow {
	for _, f := range allFlows() {
		if f.ID == id {
			return f
		}
	}
	return nil
}

func saveFlow(f *Flow) error {
	if f.ID == "" {
		f.ID = randID()
	}
	if strings.ContainsAny(f.ID, `/\.`) {
		return errors.New("bad flow id")
	}
	for _, b := range builtinFlows {
		if b.ID == f.ID {
			return errors.New("built-in templates can't be changed; duplicate it first")
		}
	}
	if err := f.validate(); err != nil {
		return err
	}
	f.BuiltIn = false
	os.MkdirAll(flowsDir(), 0o755)
	return writeJSON(filepath.Join(flowsDir(), f.ID+".json"), f)
}

func deleteFlow(id string) error {
	for _, l := range cfg().Libraries {
		if l.FlowID == id {
			return fmt.Errorf("library %q still uses this flow", l.Name)
		}
	}
	if strings.ContainsAny(id, `/\.`) {
		return errors.New("bad flow id")
	}
	return os.Remove(filepath.Join(flowsDir(), id+".json"))
}
