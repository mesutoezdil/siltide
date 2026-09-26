package query

import (
	"fmt"
	"strings"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/events"
)

// Filter is the search language: free words match names, pods, and users;
// key:value pairs narrow one field; key<op>value compares a number
// (util>80, temp>=70, mem<50, mem_used>8G). A leading ! negates any of the
// three. Keys: dev (index or label), vendor, node, name, user, ns, pod,
// workload, container, sev, kind, and state. Numbers: any device metric plus
// util, mem (percent used), health, procs, and pid.
type Filter struct {
	words    []string
	negWords []string
	keyed    map[string][]string
	negKeyed map[string][]string
	cmps     []comparison
}

// comparison is one numeric clause, "util > 80".
type comparison struct {
	key string
	op  string
	val float64
	neg bool
}

// cmpOps are tried longest first so that >= does not read as >.
var cmpOps = []string{">=", "<=", "!=", "==", ">", "<", "="}

// ParseFilter reads "python ns:ml dev:3 util>80 !vendor:amd".
func ParseFilter(s string) Filter {
	f := Filter{keyed: map[string][]string{}, negKeyed: map[string][]string{}}
	for _, tok := range strings.Fields(s) {
		neg := false
		if rest, ok := strings.CutPrefix(tok, "!"); ok && rest != "" && !strings.HasPrefix(rest, "=") {
			tok, neg = rest, true
		}
		if c, ok := parseComparison(tok); ok {
			c.neg = neg
			f.cmps = append(f.cmps, c)
			continue
		}
		tok = strings.ToLower(tok)
		if k, v, ok := strings.Cut(tok, ":"); ok && v != "" {
			m := f.keyed
			if neg {
				m = f.negKeyed
			}
			k = canonKey(k)
			m[k] = append(m[k], v)
			continue
		}
		if neg {
			f.negWords = append(f.negWords, tok)
			continue
		}
		f.words = append(f.words, tok)
	}
	return f
}

// parseComparison reads "temp>=70". It fails on anything that is not a key,
// an operator, and a number, so plain words fall through to word matching.
func parseComparison(tok string) (comparison, bool) {
	for _, op := range cmpOps {
		k, v, ok := strings.Cut(tok, op)
		if !ok || k == "" || v == "" {
			continue
		}
		n, err := events.ParseValue(strings.ToUpper(v))
		if err != nil {
			return comparison{}, false
		}
		if op == "=" {
			op = "=="
		}
		return comparison{key: canonKey(strings.ToLower(k)), op: op, val: n}, true
	}
	return comparison{}, false
}

// canonKey folds the spellings of one key onto the name the filter uses.
func canonKey(k string) string {
	switch k {
	case "gpu", "device", "idx":
		return "dev"
	case "namespace":
		return "ns"
	case "severity":
		return "sev"
	}
	return k
}

// Empty reports whether the filter matches everything.
func (f Filter) Empty() bool {
	return len(f.words) == 0 && len(f.negWords) == 0 && len(f.keyed) == 0 && len(f.negKeyed) == 0 && len(f.cmps) == 0
}

func hasWord(w string, values []string) bool {
	for _, v := range values {
		if strings.Contains(strings.ToLower(v), w) {
			return true
		}
	}
	return false
}

func anyWord(wants []string, values []string) bool {
	for _, w := range wants {
		if hasWord(w, values) {
			return true
		}
	}
	return false
}

func (f Filter) want(key string, values ...string) bool {
	if w, ok := f.keyed[key]; ok && !anyWord(w, values) {
		return false
	}
	if w, ok := f.negKeyed[key]; ok && anyWord(w, values) {
		return false
	}
	return true
}

// Words reports whether the free words of the filter match these fields, for
// a row that is not a device or a process: a pod, a workload, a node.
func (f Filter) Words(fields ...string) bool { return f.words0(fields...) }

func (f Filter) words0(fields ...string) bool {
	for _, w := range f.words {
		if !hasWord(w, fields) {
			return false
		}
	}
	for _, w := range f.negWords {
		if hasWord(w, fields) {
			return false
		}
	}
	return true
}

// numbers reports whether every comparison holds. A number the row does not
// carry never matches, the way a missing metric never fires a rule.
func (f Filter) numbers(num func(string) (float64, bool)) bool {
	for _, c := range f.cmps {
		v, ok := num(c.key)
		if !ok {
			return false
		}
		if events.Compare(v, c.op, c.val) == c.neg {
			return false
		}
	}
	return true
}

// deviceNum resolves a number key against a device.
func deviceNum(d device.Device, key string) (float64, bool) {
	switch key {
	case "dev", "index":
		return float64(d.Index), true
	case "health":
		return float64(d.Health), true
	case "procs":
		return float64(len(d.Procs)), true
	case "mem":
		return d.MemPercent()
	}
	return d.Metrics.Get(device.Metric(key))
}

// procNum resolves a number key against a process, then against its device,
// so util>80 on the process tab still reads the device when the process
// itself reports no utilization.
func procNum(d device.Device, p device.Process, key string) (float64, bool) {
	switch key {
	case "pid":
		return float64(p.PID), true
	case "mem":
		used, ok := p.Metrics.Get(device.MemUsed)
		total, ok2 := d.Metrics.Get(device.MemTotal)
		if ok && ok2 && total > 0 {
			return used / total * 100, true
		}
		return 0, false
	}
	if v, ok := p.Metrics.Get(device.Metric(key)); ok {
		return v, true
	}
	return deviceNum(d, key)
}

// procKeys are the process fields a device can be narrowed by.
var procKeys = []string{"user", "ns", "pod", "workload", "container"}

// procField reads one process field by filter key.
func procField(p device.Process, key string) []string {
	switch key {
	case "user":
		return []string{p.User}
	case "ns":
		return []string{p.Namespace}
	case "pod":
		return []string{p.Pod}
	case "workload":
		return []string{p.Workload}
	case "container":
		return []string{p.Container}
	}
	return nil
}

// Device reports whether d passes. Process-level keys (user, ns, pod, workload, and container)
// pass when any process of the device matches, and a negated one drops the device when any
// process matches it.
func (f Filter) Device(d device.Device) bool {
	if !f.want("dev", fmt.Sprint(d.Index), d.Label()) || !f.want("vendor", string(d.Vendor)) ||
		!f.want("node", d.Node, "local") || !f.want("name", d.Name) || !f.want("state", string(d.State)) {
		return false
	}
	if !f.numbers(func(k string) (float64, bool) { return deviceNum(d, k) }) {
		return false
	}
	for _, k := range procKeys {
		if _, ok := f.keyed[k]; ok {
			hit := false
			for _, p := range d.Procs {
				if f.Process(d, p) {
					hit = true
					break
				}
			}
			if !hit {
				return false
			}
		}
		if w, ok := f.negKeyed[k]; ok {
			for _, p := range d.Procs {
				if anyWord(w, procField(p, k)) {
					return false
				}
			}
		}
	}
	if len(f.words) == 0 && len(f.negWords) == 0 {
		return true
	}
	fields := []string{d.Name, d.Node, string(d.Vendor), d.ID, d.Allocated}
	for _, p := range d.Procs {
		fields = append(fields, p.Name, p.User, p.Pod, p.Namespace, p.Workload)
	}
	return f.words0(fields...)
}

// Process reports whether p on d passes.
func (f Filter) Process(d device.Device, p device.Process) bool {
	return f.want("dev", fmt.Sprint(d.Index), d.Label()) && f.want("vendor", string(d.Vendor)) && f.want("node", d.Node, "local") &&
		f.want("user", p.User) && f.want("ns", p.Namespace) && f.want("pod", p.Pod) && f.want("workload", p.Workload) &&
		f.want("container", p.Container) && f.want("name", p.Name, d.Name) &&
		f.numbers(func(k string) (float64, bool) { return procNum(d, p, k) }) &&
		f.words0(p.Name, p.User, p.Command, p.Pod, p.Namespace, p.Workload, p.Container, fmt.Sprint(p.PID), d.Name)
}

// Event reports whether e passes. Events carry no numbers, so any comparison
// in the filter empties the list rather than matching every row.
func (f Filter) Event(e events.Event) bool {
	return f.want("sev", string(e.Severity)) && f.want("kind", e.Kind) && f.want("dev", e.Label, e.Device) &&
		len(f.cmps) == 0 && f.words0(e.Message, e.Kind, string(e.Severity), e.Label)
}
