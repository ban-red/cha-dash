package truenas

import (
	"encoding/json"
	"fmt"
	"strings"
)

// API shapes, from the middleware's v25_10_0 API models (src/middlewared/
// middlewared/api/v25_10_0) and the plugins that fill the untyped parts
// (pool topology, task state). Only the fields Cha Dash reads.

// systemInfo is system.info.
type systemInfo struct {
	Version       string  `json:"version"`
	Hostname      string  `json:"hostname"`
	Physmem       float64 `json:"physmem"`
	Model         string  `json:"model"`
	Cores         int     `json:"cores"`
	PhysicalCores int     `json:"physical_cores"`
	Loadavg       []num   `json:"loadavg"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	Boottime      tnTime  `json:"boottime"`
	EccMemory     *bool   `json:"ecc_memory"`
}

// me is auth.me: who the key belongs to and its roles.
type me struct {
	Username  string `json:"pw_name"`
	Privilege struct {
		Roles    strSet `json:"roles"`
		WebShell bool   `json:"web_shell"`
	} `json:"privilege"`
}

// pool is a PoolEntry of pool.query.
type pool struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	GUID         string  `json:"guid"`
	Status       string  `json:"status"` // ONLINE, DEGRADED, FAULTED, OFFLINE, UNAVAIL, REMOVED, SUSPENDED
	Path         string  `json:"path"`
	Healthy      bool    `json:"healthy"`
	Warning      bool    `json:"warning"`
	StatusCode   *string `json:"status_code"`
	StatusDetail *string `json:"status_detail"`
	Size         num     `json:"size"`
	Allocated    num     `json:"allocated"`
	Free         num     `json:"free"`
	Fragment     num     `json:"fragmentation"` // a percentage, as a string
	Scan         *struct {
		Function   string `json:"function"` // SCRUB, RESILVER
		State      string `json:"state"`    // SCANNING, FINISHED, CANCELED
		EndTime    tnTime `json:"end_time"`
		Percentage num    `json:"percentage"`
		Errors     num    `json:"errors"`
	} `json:"scan"`
	Topology *struct {
		Data    []vdev `json:"data"`
		Log     []vdev `json:"log"`
		Cache   []vdev `json:"cache"`
		Spare   []vdev `json:"spare"`
		Special []vdev `json:"special"`
		Dedup   []vdev `json:"dedup"`
	} `json:"topology"`
}

// vdev is one node of a pool's topology: a RAIDZ, MIRROR or DISK.
type vdev struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	GUID   string `json:"guid"`
	Status string `json:"status"`
	Stats  struct {
		ReadErrors     num `json:"read_errors"`
		WriteErrors    num `json:"write_errors"`
		ChecksumErrors num `json:"checksum_errors"`
	} `json:"stats"`
	Children []vdev  `json:"children"`
	Disk     *string `json:"disk"` // the disk's name (sda), for a DISK
}

func (v vdev) errors() int {
	return int(v.Stats.ReadErrors.v + v.Stats.WriteErrors.v + v.Stats.ChecksumErrors.v)
}

// leaves are a vdev's disks, depth first.
func (v vdev) leaves() []vdev {
	if v.Type == "DISK" || len(v.Children) == 0 {
		return []vdev{v}
	}
	var out []vdev
	for _, c := range v.Children {
		out = append(out, c.leaves()...)
	}
	return out
}

// prop is a ZFS property: parsed, rawvalue, value, source.
type prop struct {
	Parsed json.RawMessage `json:"parsed"`
	Raw    string          `json:"rawvalue"`
}

func (p *prop) bytes() (float64, bool) {
	if p == nil {
		return 0, false
	}
	var n num
	if len(p.Parsed) > 0 {
		_ = json.Unmarshal(p.Parsed, &n)
	}
	if !n.ok && p.Raw != "" {
		_ = json.Unmarshal([]byte(`"`+p.Raw+`"`), &n)
	}
	return n.v, n.ok
}

// dataset is a PoolDatasetEntry of pool.dataset.query, flat.
type dataset struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"` // FILESYSTEM, VOLUME
	Name       string  `json:"name"`
	Pool       string  `json:"pool"`
	Encrypted  bool    `json:"encrypted"`
	Locked     bool    `json:"locked"`
	Mountpoint *string `json:"mountpoint"`
	Used       *prop   `json:"used"`
	Available  *prop   `json:"available"`
	Quota      *prop   `json:"quota"`
	SnapUsed   *prop   `json:"usedbysnapshots"`
}

// hidden: the pool's root, the system dataset and the apps dataset, which
// the pool and the apps already stand for.
func (d dataset) hidden() bool {
	if !strings.Contains(d.Name, "/") {
		return true
	}
	for _, part := range strings.Split(d.Name, "/")[1:] {
		if strings.HasPrefix(part, ".") || part == "ix-apps" || part == "ix-applications" {
			return true
		}
	}
	return false
}

// disk is a DiskEntry of disk.query.
type disk struct {
	Identifier   string  `json:"identifier"`
	Name         string  `json:"name"`
	Serial       string  `json:"serial"`
	Size         num     `json:"size"`
	Model        *string `json:"model"`
	RotationRate *int    `json:"rotationrate"`
	Type         *string `json:"type"` // HDD, SSD
	ZFSGUID      *string `json:"zfs_guid"`
	Bus          string  `json:"bus"`
	Pool         *string `json:"pool"`
}

// app is an AppEntry of app.query.
type app struct {
	Name             string `json:"name"`
	ID               string `json:"id"`
	State            string `json:"state"` // CRASHED, DEPLOYING, RUNNING, STOPPED, STOPPING
	UpgradeAvailable bool   `json:"upgrade_available"`
	LatestVersion    string `json:"latest_version"`
	ImageUpdates     bool   `json:"image_updates_available"`
	CustomApp        bool   `json:"custom_app"`
	HumanVersion     string `json:"human_version"`
	Version          string `json:"version"`
	Metadata         struct {
		AppVersion string `json:"app_version"`
		Name       string `json:"name"`
		Title      string `json:"title"`
		Train      string `json:"train"`
	} `json:"metadata"`
	Portals map[string]string `json:"portals"`
}

// taskState is zettarepl's state of a snapshot or replication task:
// PENDING, WAITING, RUNNING, FINISHED, ERROR or HOLD, when it was set, and
// the error, reason or last snapshot.
type taskState struct {
	State        string   `json:"state"`
	Datetime     tnTime   `json:"datetime"`
	Error        string   `json:"error"`
	Reason       string   `json:"reason"`
	LastSnapshot string   `json:"last_snapshot"`
	Warnings     []string `json:"warnings"`
}

// cron is a task's schedule.
type cron struct {
	Minute string `json:"minute"`
	Hour   string `json:"hour"`
	Dom    string `json:"dom"`
	Month  string `json:"month"`
	Dow    string `json:"dow"`
}

// String is cron's five fields.
func (c *cron) String() string {
	if c == nil {
		return ""
	}
	f := func(s string) string {
		if s == "" {
			return "*"
		}
		return s
	}
	return fmt.Sprintf("%s %s %s %s %s", f(c.Minute), f(c.Hour), f(c.Dom), f(c.Month), f(c.Dow))
}

// snapshotTask is a PoolSnapshotTaskEntry of pool.snapshottask.query.
type snapshotTask struct {
	ID            int       `json:"id"`
	Dataset       string    `json:"dataset"`
	Recursive     bool      `json:"recursive"`
	LifetimeValue int       `json:"lifetime_value"`
	LifetimeUnit  string    `json:"lifetime_unit"`
	Enabled       bool      `json:"enabled"`
	Schedule      *cron     `json:"schedule"`
	State         taskState `json:"state"`
}

// replication is a ReplicationEntry of replication.query.
type replication struct {
	ID             int      `json:"id"`
	Name           string   `json:"name"`
	Direction      string   `json:"direction"` // PUSH, PULL
	Transport      string   `json:"transport"` // SSH, SSH+NETCAT, LOCAL
	SourceDatasets []string `json:"source_datasets"`
	TargetDataset  string   `json:"target_dataset"`
	Recursive      bool     `json:"recursive"`
	Exclude        []string `json:"exclude"`
	Auto           bool     `json:"auto"`
	Enabled        bool     `json:"enabled"`
	Schedule       *cron    `json:"schedule"`
	SnapshotTasks  []struct {
		ID       int   `json:"id"`
		Schedule *cron `json:"schedule"`
	} `json:"periodic_snapshot_tasks"`
	Retention string `json:"retention_policy"`
	SSH       *struct {
		Attributes struct {
			Host string `json:"host"`
		} `json:"attributes"`
	} `json:"ssh_credentials"`
	State taskState `json:"state"`
}

// schedule is when the task runs: its own schedule, else (a task that
// runs after its snapshot tasks) the first snapshot task's.
func (r replication) schedule() string {
	if r.Schedule != nil {
		return r.Schedule.String()
	}
	for _, t := range r.SnapshotTasks {
		if t.Schedule != nil {
			return t.Schedule.String()
		}
	}
	return ""
}

// alert is an Alert of alert.list.
type alert struct {
	UUID      string          `json:"uuid"`
	Klass     string          `json:"klass"`
	Source    string          `json:"source"`
	Level     string          `json:"level"` // INFO, NOTICE, WARNING, ERROR, CRITICAL, ALERT, EMERGENCY
	Args      json.RawMessage `json:"args"`
	Datetime  tnTime          `json:"datetime"`
	Dismissed bool            `json:"dismissed"`
	Formatted *string         `json:"formatted"`
	Text      string          `json:"text"`
}

func (a alert) message() string {
	if a.Formatted != nil && *a.Formatted != "" {
		return strings.TrimSpace(*a.Formatted)
	}
	return strings.TrimSpace(a.Text)
}

func (a alert) arg(k string) string {
	var m map[string]any
	if json.Unmarshal(a.Args, &m) != nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

var levelRank = map[string]int{"INFO": 0, "NOTICE": 1, "WARNING": 2, "ERROR": 3, "CRITICAL": 4, "ALERT": 5, "EMERGENCY": 6}
