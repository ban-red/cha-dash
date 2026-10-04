package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

// One container as both sources report it, joined.
type joined struct {
	Name          string
	UnraidID      string // local part of the PrefixedID
	DockerID      string
	Manager       string // net.unraid.docker.managed: dockerman (template) or composeman
	UnraidState   string // RUNNING / EXITED (the API folds Docker's "created" into EXITED)
	DockerState   string // running / exited / created
	AutoStart     bool
	AutoStartOrd  *int
	AutoStartWait int
	Restart       string // Docker HostConfig.RestartPolicy.Name
	Desired       string // running / stopped / unknown
	Verdict       string // ok / should-run / no-autostart / stopped-on-purpose / manual
}

type unraidContainer struct {
	ID             string            `json:"id"`
	Names          []string          `json:"names"`
	State          string            `json:"state"`
	AutoStart      bool              `json:"autoStart"`
	AutoStartOrder *int              `json:"autoStartOrder"`
	AutoStartWait  *int              `json:"autoStartWait"`
	Labels         map[string]string `json:"labels"`
}

type dockerContainer struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	State string   `json:"State"`
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// joinContainers matches the Unraid API's containers with the Docker fixture's, by id and by
// name, and works out each container's desired state.
func joinContainers(fixturesDir, dockerDir string) (rows []joined, byID, byName int, err error) {
	var u struct {
		Data struct {
			Docker struct {
				Containers []unraidContainer `json:"containers"`
			} `json:"docker"`
		} `json:"data"`
	}
	if err = readJSON(filepath.Join(fixturesDir, "jupiter", "docker.json"), &u); err != nil {
		return
	}
	var d []dockerContainer
	if err = readJSON(filepath.Join(dockerDir, "containers.json"), &d); err != nil {
		return
	}
	dID, dName := map[string]dockerContainer{}, map[string]dockerContainer{}
	for _, c := range d {
		dID[c.ID] = c
		dName[strings.TrimPrefix(c.Names[0], "/")] = c
	}
	for _, c := range u.Data.Docker.Containers {
		name := strings.TrimPrefix(c.Names[0], "/")
		j := joined{Name: name, UnraidID: localID(c.ID), Manager: c.Labels["net.unraid.docker.managed"], UnraidState: c.State,
			AutoStart: c.AutoStart, AutoStartOrd: c.AutoStartOrder}
		if c.AutoStartWait != nil {
			j.AutoStartWait = *c.AutoStartWait
		}
		dc, okID := dID[j.UnraidID]
		if okID {
			byID++
		}
		if dn, ok := dName[name]; ok {
			byName++
			if !okID {
				dc = dn
			}
		}
		j.DockerID, j.DockerState = dc.ID, dc.State
		var insp struct {
			HostConfig struct {
				RestartPolicy struct{ Name string }
			}
		}
		if readJSON(filepath.Join(dockerDir, "inspect", name+".json"), &insp) == nil {
			j.Restart = insp.HostConfig.RestartPolicy.Name
		}
		j.Desired, j.Verdict = desired(j)
		rows = append(rows, j)
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].Name < rows[b].Name })
	return
}

// desired: Unraid template containers (dockerman) run with restart policy "no" and are started
// from Unraid's autostart list, so autostart is their desired state. Everything else (Compose
// Manager stacks) falls back to Docker's restart policy.
func desired(j joined) (string, string) {
	running := j.DockerState == "running" || (j.DockerState == "" && j.UnraidState == "RUNNING")
	dockerRestarts := j.Restart == "always" || j.Restart == "unless-stopped"
	if j.Manager == "dockerman" {
		switch {
		case j.AutoStart && running:
			return "running", "ok"
		case j.AutoStart && !running:
			return "running", "should-run"
		case running && dockerRestarts:
			return "running", "ok" // not on Unraid's list, but Docker brings it back itself
		case running:
			return "stopped", "no-autostart" // running now, but nothing starts it after a reboot
		default:
			return "stopped", "stopped-on-purpose"
		}
	}
	switch j.Restart {
	case "always", "unless-stopped":
		if running {
			return "running", "ok"
		}
		// Docker restarts these after a crash, so an exited one was stopped by someone.
		return "stopped", "stopped-on-purpose"
	default:
		if running {
			return "unknown", "manual"
		}
		return "unknown", "stopped-on-purpose"
	}
}

func analyze(fixturesDir, dockerDir string, w io.Writer) error {
	rows, byID, byName, err := joinContainers(fixturesDir, dockerDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "containers: %d from the Unraid API · joined with the Docker fixture by id: %d, by name: %d\n\n", len(rows), byID, byName)

	count := map[string]int{}
	for _, r := range rows {
		count[fmt.Sprintf("%s · autostart=%v · %s · restart=%s → %s", r.Manager, r.AutoStart, r.DockerState, orDash(r.Restart), r.Verdict)]++
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%3d  %s\n", count[k], k)
	}

	fmt.Fprintln(w, "\nnot quiet (should run but doesn't, or runs without autostart):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "container\tmanager\tunraid state\tdocker state\tautostart\trestart\tverdict")
	for _, r := range rows {
		if r.Verdict == "should-run" || r.Verdict == "no-autostart" || r.Verdict == "manual" {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%v\t%s\t%s\n", r.Name, r.Manager, r.UnraidState, r.DockerState, r.AutoStart, orDash(r.Restart), r.Verdict)
		}
	}
	return tw.Flush()
}
