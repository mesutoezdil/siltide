// Package history is siltide's time machine: a per-device series of
// downsampled points, kept in memory and optionally appended to plain text
// files so a restart does not lose the past.
//
// File format, one point per line, one file per hour:
//
//	<unix ms> <device id> <util> <mem_used> <mem_total> <temp> <power> <clock_core> <throttle> <crc32>
//
// Missing values are written as "-". Throttle bits are OR-ed over every
// sample since the previous point, so a short burst is never lost to
// downsampling. The CRC covers everything before it;
// a torn or corrupted line is skipped on load. A lock file keeps 2 siltide
// processes from writing the same directory: the second keeps its history
// in memory and reports it.
package history

import (
	"bufio"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moezdil/siltide/internal/device"
)

// Tracked are the metrics kept in history, in column order.
var Tracked = []device.Metric{device.Util, device.MemUsed, device.MemTotal, device.Temp, device.Power, device.ClockCore, device.Throttle}

// Point is one sample of a device.
type Point struct {
	T time.Time
	V [7]float32 // Tracked order; NaN when missing
}

// Get returns the tracked metric k.
func (p Point) Get(k device.Metric) (float64, bool) {
	for i, t := range Tracked {
		if t == k {
			v := float64(p.V[i])
			return v, !math.IsNaN(v)
		}
	}
	return 0, false
}

// Options configure a store.
type Options struct {
	Keep       time.Duration
	Resolution time.Duration
	Dir        string // "" keeps history in memory only
	MaxDisk    int64  // bytes; 0 means unlimited
}

// Store holds the series.
type Store struct {
	o  Options
	mu sync.Mutex

	series map[string][]Point
	last   time.Time
	// throttle accumulates Throttle bits between points, per device.
	throttle map[string]float64
	file     *os.File
	hour     string
	lock     *os.File
	// Warning explains why history is not persisted, or "".
	Warning string
}

// ErrLocked means another siltide owns the directory.
var ErrLocked = errors.New("history directory is in use by another siltide")

// Open creates a store and loads what the directory still holds.
func Open(o Options) (*Store, error) {
	s := &Store{o: o, series: map[string][]Point{}, throttle: map[string]float64{}}
	if o.Dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	lock, err := acquire(filepath.Join(o.Dir, "lock"))
	if err != nil {
		s.o.Dir = ""
		s.Warning = "history kept in memory: " + err.Error()
		return s, nil
	}
	s.lock = lock
	return s, nil
}

// Record stores one point per device when a resolution step has passed.
func (s *Store) Record(t time.Time, devs []device.Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range devs {
		if v, ok := d.Metrics.Get(device.Throttle); ok {
			s.throttle[d.ID] = float64(int(s.throttle[d.ID]) | int(v))
		}
	}
	if t.Sub(s.last) < s.o.Resolution {
		return
	}
	s.last = t
	var w *bufio.Writer
	if s.o.Dir != "" {
		if f := s.fileFor(t); f != nil {
			w = bufio.NewWriter(f)
		}
	}
	cut := t.Add(-s.o.Keep)
	for _, d := range devs {
		p := Point{T: t}
		for i, k := range Tracked {
			p.V[i] = float32(math.NaN())
			if v, ok := d.Metrics.Get(k); ok {
				p.V[i] = float32(v)
			}
		}
		if _, ok := d.Metrics.Get(device.Throttle); ok {
			p.V[len(Tracked)-1] = float32(s.throttle[d.ID])
			delete(s.throttle, d.ID)
		}
		s.series[d.ID] = trim(append(s.series[d.ID], p), cut)
		if w != nil {
			_, _ = w.WriteString(p.line(d.ID))
		}
	}
	if w != nil {
		_ = w.Flush()
	}
	for id, ps := range s.series {
		if len(ps) == 0 || ps[len(ps)-1].T.Before(cut) {
			delete(s.series, id)
		}
	}
}

func trim(ps []Point, cut time.Time) []Point {
	i := sort.Search(len(ps), func(i int) bool { return !ps[i].T.Before(cut) })
	return ps[i:]
}

func (p Point) line(id string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s", p.T.UnixMilli(), id)
	for _, v := range p.V {
		if math.IsNaN(float64(v)) {
			b.WriteString(" -")
		} else {
			fmt.Fprintf(&b, " %g", v)
		}
	}
	fmt.Fprintf(&b, " %08x\n", crc32.ChecksumIEEE([]byte(b.String())))
	return b.String()
}

func parseLine(l string) (string, Point, bool) {
	f := strings.Fields(l)
	if len(f) != 3+len(Tracked) {
		return "", Point{}, false
	}
	body := l[:strings.LastIndex(l, " ")]
	if fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(body))) != f[len(f)-1] {
		return "", Point{}, false
	}
	ms, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return "", Point{}, false
	}
	p := Point{T: time.UnixMilli(ms)}
	for i := range Tracked {
		p.V[i] = float32(math.NaN())
		if f[2+i] != "-" {
			if v, err := strconv.ParseFloat(f[2+i], 32); err == nil {
				p.V[i] = float32(v)
			}
		}
	}
	return f[1], p, true
}

// fileFor returns the open file for t's hour, rotating and pruning as
// needed. Errors leave history in memory only.
func (s *Store) fileFor(t time.Time) *os.File {
	hour := t.UTC().Format("2006010215")
	if s.file != nil && s.hour == hour {
		return s.file
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	f, err := os.OpenFile(filepath.Join(s.o.Dir, hour+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	s.file, s.hour = f, hour
	s.prune(t)
	return f
}

// prune removes files past retention, then the oldest until the directory
// fits the disk cap. The current hour is never removed.
func (s *Store) prune(now time.Time) {
	cut := now.Add(-s.o.Keep).UTC().Format("2006010215")
	files, _ := filepath.Glob(filepath.Join(s.o.Dir, "*.log"))
	sort.Strings(files)
	var total int64
	var sizes []int64
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".log")
		if name < cut {
			_ = os.Remove(f)
			continue
		}
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		total += st.Size()
		sizes = append(sizes, st.Size())
		files = append(files[:len(sizes)-1], f) // compact in place
	}
	files = files[:len(sizes)]
	for i := 0; s.o.MaxDisk > 0 && total > s.o.MaxDisk && i < len(files)-1; i++ {
		_ = os.Remove(files[i])
		total -= sizes[i]
	}
}

func (s *Store) load() error {
	files, err := filepath.Glob(filepath.Join(s.o.Dir, "*.log"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	cut := time.Now().Add(-s.o.Keep)
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			id, p, ok := parseLine(sc.Text())
			if ok && !p.T.Before(cut) {
				s.series[id] = append(s.series[id], p)
			}
		}
		_ = fh.Close() // a torn last line fails its CRC and is skipped
	}
	for id := range s.series {
		ps := s.series[id]
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].T.Before(ps[j].T) })
		s.series[id] = ps
		if len(ps) > 0 && ps[len(ps)-1].T.After(s.last) {
			s.last = ps[len(ps)-1].T
		}
	}
	return nil
}

// Close flushes the current file and releases the lock.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	if s.lock != nil {
		release(s.lock)
		s.lock = nil
	}
}

// Recent returns up to n latest values of k, oldest first. Gaps are NaN.
func (s *Store) Recent(id string, k device.Metric, n int) []float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps := s.series[id]
	if len(ps) > n {
		ps = ps[len(ps)-n:]
	}
	out := make([]float64, len(ps))
	for i, p := range ps {
		v, ok := p.Get(k)
		if !ok {
			v = math.NaN()
		}
		out[i] = v
	}
	return out
}

// Series returns the points of one device, oldest first.
func (s *Store) Series(id string) []Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Point(nil), s.series[id]...)
}

// Between returns the points of one device inside [from, to].
func (s *Store) Between(id string, from, to time.Time) []Point {
	ps := s.Series(id)
	i := sort.Search(len(ps), func(i int) bool { return !ps[i].T.Before(from) })
	j := sort.Search(len(ps), func(i int) bool { return ps[i].T.After(to) })
	if i > j {
		return nil
	}
	return ps[i:j]
}

// At returns each device's last point at or before t.
func (s *Store) At(t time.Time) map[string]Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Point{}
	for id, ps := range s.series {
		i := sort.Search(len(ps), func(i int) bool { return ps[i].T.After(t) })
		if i > 0 {
			out[id] = ps[i-1]
		}
	}
	return out
}

// Span is the oldest and newest time held for any device.
func (s *Store) Span() (time.Time, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lo, hi time.Time
	for _, ps := range s.series {
		if len(ps) == 0 {
			continue
		}
		if lo.IsZero() || ps[0].T.Before(lo) {
			lo = ps[0].T
		}
		if ps[len(ps)-1].T.After(hi) {
			hi = ps[len(ps)-1].T
		}
	}
	return lo, hi
}

// IDs lists the devices with history.
func (s *Store) IDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.series))
	for id := range s.series {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Resolution is the step between points.
func (s *Store) Resolution() time.Duration { return s.o.Resolution }
