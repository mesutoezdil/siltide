package smi

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

var leadingNum = regexp.MustCompile(`^[-+]?\d+(?:\.\d+)?`)

// num parses the leading number of s ("37 W", "44 C", "35 ℃", "0.0 %").
// N/A, NA and empty values report false.
func num(s string) (float64, bool) {
	m := leadingNum.FindString(strings.TrimSpace(s))
	if m == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(m, 64)
	return v, err == nil
}

// deviceIndex reads the index column of a table row. A vendor tool prints a
// small non-negative number there, so anything else, a negative number above
// all, is a line that only looks like a device row.
func deviceIndex(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 || n > maxDeviceIndex {
		return 0, false
	}
	return n, true
}

// maxDeviceIndex is past any real machine and short of a bus address read as
// an index by mistake.
const maxDeviceIndex = 4096

// firstWord is the command name at the front of a process line, or "" when
// the tool printed the column empty.
func firstWord(s string) string {
	fs := strings.Fields(s)
	if len(fs) == 0 {
		return ""
	}
	return fs[0]
}

// set stores s as metric k scaled to the metric's unit, when s is numeric.
// A scale of 1<<20 turns MiB into bytes.
func set(m device.Metrics, k device.Metric, s string, scale float64) {
	if v, ok := num(s); ok {
		m[k] = v * scale
	}
}

var unitScale = map[string]float64{
	"b": 1, "kb": 1 << 10, "kib": 1 << 10, "mb": 1 << 20, "mib": 1 << 20,
	"gb": 1 << 30, "gib": 1 << 30, "tb": 1 << 40, "tib": 1 << 40,
}

// setBytes stores "32768 MiB" or "32768MiB" as bytes. A bare number is in
// def ("mib", "b", ...). Vendor tools say MB when they mean MiB.
func setBytes(m device.Metrics, k device.Metric, s, def string) {
	s = strings.TrimSpace(s)
	v, ok := num(s)
	if !ok || v < 0 {
		return
	}
	unit := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, leadingNum.FindString(s))))
	if unit == "" {
		unit = def
	}
	if scale, ok := unitScale[unit]; ok {
		m[k] = v * scale
	}
}

// block is one device section of "key : value" output. The first
// occurrence of a key wins, so nested repeats (Board vs Chip temperature)
// do not overwrite the headline value.
type block map[string]string

func (b block) set(k, v string) {
	if _, ok := b[k]; !ok {
		b[k] = v
	}
}

// first returns the value of the first present key.
func (b block) first(keys ...string) string {
	for _, k := range keys {
		if v, ok := b[k]; ok {
			return v
		}
	}
	return ""
}

// kvBlocks splits text into blocks, starting a new one on every line that
// start matches. Its first submatch becomes the "#" key.
func kvBlocks(text string, start *regexp.Regexp) []block {
	var out []block
	var cur block
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if m := start.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			cur = block{}
			if len(m) > 1 {
				cur["#"] = m[1]
			}
			out = append(out, cur)
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		cur.set(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return out
}

// cells splits a "| a | b | c |" table row into trimmed cells.
func cells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return nil
	}
	parts := strings.Split(strings.Trim(line, "|"), "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

var (
	busID   = regexp.MustCompile(`(?i)^[0-9a-f]{4,8}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
	tempC   = regexp.MustCompile(`\b(\d+)C\b`)
	percent = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%`)
)

func lines(text string) []string { return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") }
