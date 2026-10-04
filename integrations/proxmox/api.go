package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"dash.cha.sh/internal/integration"
)

// The PVE API's shapes, only the fields the integration reads. PVE is loose
// with types (a flag may be 1, "1" or true), so flags decode through num.

// num is a JSON number that also accepts numeric strings, booleans and null.
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	switch s {
	case "", "null", "false":
		*n = 0
		return nil
	case "true":
		*n = 1
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*n = 0 // a flag PVE spells some other way: treat it as unset
		return nil
	}
	*n = num(f)
	return nil
}

func (n num) bool() bool { return n != 0 }

// versionInfo is GET /version.
type versionInfo struct {
	Release string `json:"release"` // "9.0"
	Version string `json:"version"` // "9.0.9"
}

// statusEntry is one row of GET /cluster/status: the cluster itself (type
// cluster) or a node.
type statusEntry struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // cluster or node
	Name    string `json:"name"`
	Nodes   num    `json:"nodes"`
	Quorate num    `json:"quorate"`
	Version num    `json:"version"`
	IP      string `json:"ip"`
	Online  num    `json:"online"`
	Local   num    `json:"local"`
}

// resource is one row of GET /cluster/resources: a node, a guest (qemu or
// lxc), a storage (one row per node it's available on), or an SDN/network
// entry the integration ignores.
type resource struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Node       string `json:"node"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	VMID       num    `json:"vmid"`
	CPU        num    `json:"cpu"`
	MaxCPU     num    `json:"maxcpu"`
	Mem        num    `json:"mem"`
	MaxMem     num    `json:"maxmem"`
	Disk       num    `json:"disk"`
	MaxDisk    num    `json:"maxdisk"`
	Uptime     num    `json:"uptime"`
	Template   num    `json:"template"`
	HAState    string `json:"hastate"`
	Lock       string `json:"lock"`
	Tags       string `json:"tags"`
	Storage    string `json:"storage"`
	PluginType string `json:"plugintype"`
	Shared     num    `json:"shared"`
	Content    string `json:"content"`
}

func (r resource) guestKey() string { return r.Type + "/" + strconv.Itoa(int(r.VMID)) }

// task is one row of GET /cluster/tasks. Status is "OK", "WARNINGS: n" or the
// error text; empty while the task runs.
type task struct {
	UPID      string `json:"upid"`
	Type      string `json:"type"`
	ID        string `json:"id"`
	Node      string `json:"node"`
	User      string `json:"user"`
	Status    string `json:"status"`
	StartTime int64  `json:"starttime"`
	EndTime   int64  `json:"endtime"`
}

func (t task) running() bool  { return t.EndTime == 0 && t.Status == "" }
func (t task) ok() bool       { return t.Status == "OK" || strings.HasPrefix(t.Status, "WARNINGS") }
func (t task) finished() bool { return !t.running() }

// backupJob is one entry of GET /cluster/backup.
type backupJob struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Enabled  *num   `json:"enabled"` // absent means enabled
	Schedule string `json:"schedule"`
	Storage  string `json:"storage"`
	Node     string `json:"node"`
	VMID     string `json:"vmid"`
	All      num    `json:"all"`
	Exclude  string `json:"exclude"`
	Pool     string `json:"pool"`
	Mode     string `json:"mode"`
	NextRun  int64  `json:"next-run"`
	Comment  string `json:"comment"`
}

func (j backupJob) enabled() bool { return j.Enabled == nil || j.Enabled.bool() }

// guest is an entry of GET /cluster/backup-info/not-backed-up.
type notBackedUp struct {
	Name string `json:"name"`
	Type string `json:"type"`
	VMID num    `json:"vmid"`
}

// nodeStatus is GET /nodes/{node}/status. Its cpu reads 0: CPU comes from
// /cluster/resources.
type nodeStatus struct {
	Memory struct {
		Total     num  `json:"total"`
		Used      num  `json:"used"`
		Available *num `json:"available"` // 9.x only
	} `json:"memory"`
	Swap struct {
		Total num `json:"total"`
		Used  num `json:"used"`
	} `json:"swap"`
	RootFS struct {
		Total num `json:"total"`
		Used  num `json:"used"`
	} `json:"rootfs"`
	PVEVersion    string `json:"pveversion"` // "pve-manager/9.0.9/117b893e0e6a4fee"
	CurrentKernel struct {
		Release string `json:"release"`
	} `json:"current-kernel"`
	CPUInfo struct {
		Model string `json:"model"`
	} `json:"cpuinfo"`
}

// storageConfig is an entry of GET /storage: the parts that say where a
// storage really is.
type storageConfig struct {
	Storage     string `json:"storage"`
	Type        string `json:"type"`
	Server      string `json:"server"`
	Datastore   string `json:"datastore"`
	Fingerprint string `json:"fingerprint"`
	Export      string `json:"export"`
	Disable     num    `json:"disable"`
}

// lxcIface is an entry of GET /nodes/{node}/lxc/{vmid}/interfaces.
type lxcIface struct {
	Name   string  `json:"name"`
	HWAddr *string `json:"hwaddr"`
	Addrs  []struct {
		IP   string `json:"ip-address"`
		Type string `json:"ip-address-type"` // inet, inet6
	} `json:"ip-addresses"`
}

// agentIfaces is GET /nodes/{node}/qemu/{vmid}/agent/network-get-interfaces.
type agentIfaces struct {
	Result []struct {
		Name   string `json:"name"`
		HWAddr string `json:"hardware-address"`
		Addrs  []struct {
			IP   string `json:"ip-address"`
			Type string `json:"ip-address-type"` // ipv4, ipv6
		} `json:"ip-addresses"`
	} `json:"result"`
}

// userInfo is GET /access/users/{user}: privsep per token.
type userInfo struct {
	Tokens map[string]struct {
		Privsep num `json:"privsep"`
	} `json:"tokens"`
}

// logLine is one line of GET /nodes/{node}/tasks/{upid}/log.
type logLine struct {
	N int    `json:"n"`
	T string `json:"t"`
}

// apiError is a non-2xx answer. PVE puts the reason in the HTTP status line
// ("403 Permission check failed (/nodes/io, Sys.Modify)"); it's kept for
// warnings. It unwraps to the framework's StatusError, so a 401 or 403 on a
// call the integration can't do without still becomes an auth Repair.
type apiError struct {
	*integration.StatusError
	Reason string
}

func (e *apiError) Error() string {
	msg := e.StatusError.Error()
	if e.Reason != "" && e.Reason != http.StatusText(e.Code) {
		msg += ": " + e.Reason
	}
	return msg
}

func (e *apiError) Unwrap() error { return e.StatusError }

// forbidden reports whether err is a 403: the token lacks a privilege.
func forbidden(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == http.StatusForbidden
}

// get fetches path (under /api2/json) and decodes the envelope's data into
// dst. Secrets travel in the Authorization header only.
func (in *instance) get(ctx context.Context, path string, q url.Values, dst any) error {
	u := in.base + "/api2/json" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return &integration.ConfigError{Message: "the url can't be used: " + err.Error(), Field: "url"}
	}
	req.Header.Set("Authorization", in.auth)
	req.Header.Set("Accept", "application/json")
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := integration.CheckResponse(resp); err != nil {
		var se *integration.StatusError
		errors.As(err, &se)
		return &apiError{StatusError: se, Reason: reason(resp.Status, body)}
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("GET %s: the answer isn't PVE's JSON: %w", path, err)
	}
	if len(env.Data) == 0 || bytes.Equal(env.Data, []byte("null")) {
		return fmt.Errorf("GET %s: the answer has no data", path)
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		return fmt.Errorf("GET %s: unexpected shape: %w", path, err)
	}
	return nil
}

// reason is the text of a PVE error: the status line's reason when it's not
// the standard one, else the body's message.
func reason(status string, body []byte) string {
	code, text, _ := strings.Cut(status, " ")
	if n, err := strconv.Atoi(code); err == nil && text != http.StatusText(n) && text != "" {
		return text
	}
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		return strings.TrimSpace(e.Message)
	}
	return ""
}

// p builds an API path from segments, escaping each one.
func p(segs ...string) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}
