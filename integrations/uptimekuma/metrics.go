package uptimekuma

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// promMonitor is what /metrics says about one monitor.
type promMonitor struct {
	id, name, typ       string
	url, host, port     string
	status              float64 // NaN when absent
	responseMS          float64
	certDays, certValid float64
}

// promMetrics are the monitors in a /metrics document, by Uptime Kuma's
// monitor ID (the monitor_id label, Uptime Kuma 2) and by name (1.x has no
// ID label).
type promMetrics struct {
	byID   map[string]*promMonitor
	byName map[string][]*promMonitor
}

// lookup finds a status page monitor: by ID when the labels carry one, else
// by name when exactly one monitor has it.
func (p promMetrics) lookup(id, name string) *promMonitor {
	if m, ok := p.byID[id]; ok {
		return m
	}
	if len(p.byID) == 0 && len(p.byName[name]) == 1 {
		return p.byName[name][0]
	}
	return nil
}

// parseMetrics reads the Prometheus text format and keeps Uptime Kuma's
// monitor_* gauges. Everything else (process and Node.js metrics) is
// skipped.
func parseMetrics(r io.Reader) (promMetrics, error) {
	p := promMetrics{byID: map[string]*promMonitor{}, byName: map[string][]*promMonitor{}}
	seen := map[string]*promMonitor{} // by the full label set that identifies a monitor
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || !strings.HasPrefix(line, "monitor_") {
			continue
		}
		name, labels, value, err := parseSample(line)
		if err != nil {
			return p, fmt.Errorf("line %d: %w", n, err)
		}
		if labels["window"] != "" {
			continue // sliding-window averages; the instant values are enough
		}
		key := labels["monitor_id"] + "\x00" + labels["monitor_name"] + "\x00" + labels["monitor_type"] + "\x00" + labels["monitor_url"] + "\x00" + labels["monitor_hostname"] + "\x00" + labels["monitor_port"]
		m := seen[key]
		if m == nil {
			m = &promMonitor{
				id: labels["monitor_id"], name: labels["monitor_name"], typ: labels["monitor_type"],
				url: clean(labels["monitor_url"]), host: clean(labels["monitor_hostname"]), port: clean(labels["monitor_port"]),
				status: math.NaN(), responseMS: math.NaN(), certDays: math.NaN(), certValid: math.NaN(),
			}
			seen[key] = m
			if m.id != "" {
				p.byID[m.id] = m
			}
			p.byName[m.name] = append(p.byName[m.name], m)
		}
		switch name {
		case "monitor_status":
			m.status = value
		case "monitor_response_time":
			m.responseMS = value
		case "monitor_cert_days_remaining":
			m.certDays = value
		case "monitor_cert_is_valid":
			m.certValid = value
		}
	}
	if err := sc.Err(); err != nil {
		return p, err
	}
	return p, nil
}

// clean drops the placeholders Uptime Kuma writes for an unset field: a
// null rendered as text, and the form's default "https://".
func clean(s string) string {
	switch s {
	case "null", "undefined", "https://", "http://":
		return ""
	}
	return s
}

// parseSample splits `name{label="value",…} value [timestamp]`.
func parseSample(line string) (name string, labels map[string]string, value float64, err error) {
	labels = map[string]string{}
	i := strings.IndexAny(line, "{ \t")
	if i < 0 {
		return "", nil, 0, fmt.Errorf("no value")
	}
	name, rest := line[:i], line[i:]
	if rest[0] == '{' {
		rest = rest[1:]
		for {
			rest = strings.TrimLeft(rest, " \t,")
			if rest == "" {
				return "", nil, 0, fmt.Errorf("unterminated labels")
			}
			if rest[0] == '}' {
				rest = rest[1:]
				break
			}
			eq := strings.IndexByte(rest, '=')
			if eq < 0 || len(rest) < eq+2 || rest[eq+1] != '"' {
				return "", nil, 0, fmt.Errorf("malformed label")
			}
			lname := strings.TrimSpace(rest[:eq])
			rest = rest[eq+2:]
			var b strings.Builder
			closed := false
			for j := 0; j < len(rest); j++ {
				c := rest[j]
				if c == '\\' && j+1 < len(rest) {
					j++
					switch rest[j] {
					case 'n':
						b.WriteByte('\n')
					default:
						b.WriteByte(rest[j])
					}
					continue
				}
				if c == '"' {
					rest = rest[j+1:]
					closed = true
					break
				}
				b.WriteByte(c)
			}
			if !closed {
				return "", nil, 0, fmt.Errorf("unterminated label value")
			}
			labels[lname] = b.String()
		}
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", nil, 0, fmt.Errorf("no value")
	}
	value, err = strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", nil, 0, fmt.Errorf("value %q: %w", fields[0], err)
	}
	return name, labels, value, nil
}
