package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"dash.cha.sh/spikes/traefik/labkit"
)

// inspectDoc holds the inspect fields the hub's read model needs.
type inspectDoc struct {
	Name         string
	RestartCount int
	Config       struct {
		Image       string
		Env         []string
		Labels      map[string]string
		Healthcheck *struct{ Test []string }
	}
	State struct {
		Status     string
		Running    bool
		Restarting bool
		OOMKilled  bool
		ExitCode   int
		StartedAt  string
		FinishedAt string
		Health     *struct {
			Status        string
			FailingStreak int
		}
	}
	HostConfig struct {
		NetworkMode   string
		RestartPolicy struct{ Name string }
		PortBindings  map[string][]struct{ HostPort string }
	}
}

var webuiPort = regexp.MustCompile(`\[PORT:(\d+)\]`)

func analyze(fx string) error {
	dirs, err := os.ReadDir(fx)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if d.IsDir() {
			if err := analyzeEndpoint(filepath.Join(fx, d.Name()), d.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func analyzeEndpoint(dir, name string) error {
	var meta map[string]any
	if err := readJSON(filepath.Join(dir, "capture.json"), &meta); err != nil {
		return err
	}
	fmt.Printf("# %s\n\n", name)
	if r, _ := meta["reachable"].(bool); !r {
		fmt.Printf("Unreachable from the capture host: %v.\n\n", meta["error"])
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "inspect", "*.json"))
	var cs []inspectDoc
	for _, f := range files {
		var c inspectDoc
		if err := readJSON(f, &c); err != nil {
			return err
		}
		cs = append(cs, c)
	}
	sort.Slice(cs, func(i, j int) bool { return strings.ToLower(cs[i].Name) < strings.ToLower(cs[j].Name) })

	count := func(f func(c inspectDoc) string) string {
		m := map[string]int{}
		for _, c := range cs {
			m[f(c)]++
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] || m[keys[i]] == m[keys[j]] && keys[i] < keys[j] })
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
		}
		return strings.Join(parts, " · ")
	}
	fmt.Printf("## Summary (%d containers)\n\n", len(cs))
	fmt.Println("- state:", count(func(c inspectDoc) string { return c.State.Status }))
	fmt.Println("- health:", count(func(c inspectDoc) string {
		if c.State.Health == nil {
			return "no healthcheck"
		}
		return c.State.Health.Status
	}))
	fmt.Println("- restart policy:", count(func(c inspectDoc) string { return c.HostConfig.RestartPolicy.Name }))
	fmt.Println("- manager (net.unraid.docker.managed):", count(func(c inspectDoc) string {
		if m := c.Config.Labels["net.unraid.docker.managed"]; m != "" {
			return m
		}
		return "(none)"
	}))
	fmt.Println("- compose project:", count(func(c inspectDoc) string {
		if p := c.Config.Labels["com.docker.compose.project"]; p != "" {
			return p
		}
		return "(not compose)"
	}))
	fmt.Println("- network mode:", count(func(c inspectDoc) string { return c.HostConfig.NetworkMode }))
	restarts, oom, secretKeys, withSecret := 0, 0, 0, 0
	for _, c := range cs {
		restarts += c.RestartCount
		if c.State.OOMKilled {
			oom++
		}
		n := 0
		for _, e := range c.Config.Env {
			k, _, _ := strings.Cut(e, "=")
			if labkit.SecretLookingKey(k) {
				n++
			}
		}
		secretKeys += n
		if n > 0 {
			withSecret++
		}
	}
	fmt.Printf("- restart count total: %d · OOM-killed: %d\n", restarts, oom)
	fmt.Printf("- secret-looking env keys: %d across %d containers (values redacted)\n\n", secretKeys, withSecret)

	fmt.Println("## Containers")
	fmt.Println()
	fmt.Println("| Container | Image | App (guess) | Managed by | State | Health | Restarts · policy | Open URL (webui) |")
	fmt.Println("|---|---|---|---|---|---|---|---|")
	for _, c := range cs {
		l := c.Config.Labels
		mgr := l["net.unraid.docker.managed"]
		if p := l["com.docker.compose.project"]; p != "" {
			mgr += " · " + p + "/" + l["com.docker.compose.service"]
		}
		health := "—"
		if c.State.Health != nil {
			health = c.State.Health.Status
			if c.State.Health.FailingStreak > 0 {
				health += fmt.Sprintf(" (failing %d)", c.State.Health.FailingStreak)
			}
		}
		state := c.State.Status
		if c.State.Status == "exited" {
			state += fmt.Sprintf(" (%d)", c.State.ExitCode)
		}
		if c.State.OOMKilled {
			state += " OOM"
		}
		fmt.Printf("| `%s` | `%s` | %s | %s | %s | %s | %d · %s | %s |\n", strings.TrimPrefix(c.Name, "/"), c.Config.Image,
			appGuess(c), mgr, state, health, c.RestartCount, c.HostConfig.RestartPolicy.Name, openURL(c))
	}
	fmt.Println()

	if st, ok := meta["eventStats"].(map[string]any); ok {
		fmt.Println("## Events")
		for _, w := range []string{"past", "live"} {
			b, _ := json.Marshal(st[w])
			fmt.Printf("- %s: %s\n", w, b)
		}
		fmt.Println()
	}

	var probe []ProbeRow
	if err := readJSON(filepath.Join(dir, "probe.json"), &probe); err == nil {
		fmt.Println("## Proxy-vs-raw probe")
		fmt.Println()
		fmt.Println("| Request | Group | Status | Raw Docker would answer |")
		fmt.Println("|---|---|---|---|")
		blocked, passed := 0, 0
		for _, r := range probe {
			st := fmt.Sprint(r.Status)
			if r.Error != "" {
				st = r.Error
			}
			if r.ContentType != "" {
				st += " (" + r.ContentType + ")"
			}
			fmt.Printf("| `%s %s` | %s | %s | %s |\n", r.Method, r.Path, r.Group, st, r.RawExpect)
			if r.Group == gBlocked {
				if r.Status == 403 || r.Status == 401 || r.Status == 405 {
					blocked++
				} else {
					passed++
				}
			}
		}
		verdict := "filtering proxy"
		if blocked == 0 {
			verdict = "RAW Docker API (nothing refused)"
		} else if passed > 0 {
			verdict = fmt.Sprintf("filtering proxy, but %d endpoint(s) a strict proxy refuses reached Docker", passed)
		}
		fmt.Printf("\nVerdict: **%s** (%d refused, %d passed of the 'blocked' group).\n\n", verdict, blocked, passed)
	}
	return nil
}

var imageBase = regexp.MustCompile(`^(?:.*/)?(?:arch-)?([^/:@]+)`)

// appGuess names the logical app: an explicit homepage.name, else the compose
// project for multi-service stacks, else the image's base name.
func appGuess(c inspectDoc) string {
	if n := c.Config.Labels["homepage.name"]; n != "" {
		return n + " (label)"
	}
	if p := c.Config.Labels["com.docker.compose.project"]; p != "" {
		return p
	}
	if m := imageBase.FindStringSubmatch(c.Config.Image); m != nil {
		return strings.TrimSuffix(strings.TrimSuffix(m[1], "-server"), "-docker")
	}
	return ""
}

// openURL resolves Unraid's webui template (http://[IP]:[PORT:8989]/) to the
// host port bound for that container port.
func openURL(c inspectDoc) string {
	t := c.Config.Labels["net.unraid.docker.webui"]
	if t == "" {
		return ""
	}
	return webuiPort.ReplaceAllStringFunc(strings.ReplaceAll(t, "[IP]", "10.0.11.120"), func(m string) string {
		p := webuiPort.FindStringSubmatch(m)[1]
		if c.HostConfig.NetworkMode == "host" {
			return p
		}
		for _, proto := range []string{"/tcp", "/udp"} {
			if bs := c.HostConfig.PortBindings[p+proto]; len(bs) > 0 && bs[0].HostPort != "" {
				return bs[0].HostPort
			}
		}
		return p + "?"
	})
}

func readJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
