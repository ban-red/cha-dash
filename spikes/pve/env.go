package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Instance is one PVE endpoint from the env file. Its fields are secret or
// identifying: never print URL, TokenID or Secret, and never write them to a
// fixture. Name is the instance's nickname (from PVE_INSTANCES) and is safe.
type Instance struct {
	Name    string // nickname, lowercased: pluto, minig5, mars, io, jupiter-nas
	Key     string // the UPPERCASE key used in the env file
	URL     *url.URL
	TokenID string // user@realm!tokenname
	Secret  string // UUID
}

// User is the token's owner (user@realm), Token the token name.
func (in Instance) User() string  { u, _, _ := strings.Cut(in.TokenID, "!"); return u }
func (in Instance) Token() string { _, t, _ := strings.Cut(in.TokenID, "!"); return t }
func (in Instance) Realm() string {
	_, r, _ := strings.Cut(in.User(), "@")
	return r
}

// loadEnv parses a dotenv-style file (KEY=VALUE, # comments, optional quotes)
// without exporting anything to the process environment.
func loadEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		vals[k] = v
	}
	return vals, sc.Err()
}

// nickname maps an env instance key onto the lab's host nickname. Env keys
// can't contain '-', so JUPITERNAS / JUPITER_NAS become jupiter-nas.
func nickname(key string) string {
	n := strings.ToLower(key)
	switch strings.ReplaceAll(strings.ReplaceAll(n, "_", ""), "-", "") {
	case "jupiternas":
		return "jupiter-nas"
	case "minig5":
		return "minig5"
	}
	return strings.ReplaceAll(n, "_", "-")
}

// loadInstances reads PVE_INSTANCES and each instance's URL and token. Errors
// name the missing key, never a value.
func loadInstances(path string) ([]Instance, error) {
	vals, err := loadEnv(path)
	if err != nil {
		return nil, err
	}
	list := strings.TrimSpace(vals["PVE_INSTANCES"])
	if list == "" {
		return nil, fmt.Errorf("PVE_INSTANCES is empty")
	}
	var out []Instance
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToUpper(name)
		in := Instance{Name: nickname(name), Key: key}
		for _, f := range []string{"URL", "TOKEN_ID", "TOKEN_SECRET"} {
			if vals["PVE_"+key+"_"+f] == "" {
				return nil, fmt.Errorf("instance %s: PVE_%s_%s is missing", in.Name, key, f)
			}
		}
		u, err := url.Parse(strings.TrimRight(vals["PVE_"+key+"_URL"], "/"))
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, fmt.Errorf("instance %s: PVE_%s_URL is not an https URL", in.Name, key)
		}
		in.URL = u
		in.TokenID = vals["PVE_"+key+"_TOKEN_ID"]
		in.Secret = vals["PVE_"+key+"_TOKEN_SECRET"]
		if !strings.Contains(in.TokenID, "!") || !strings.Contains(in.TokenID, "@") {
			return nil, fmt.Errorf("instance %s: token ID is not user@realm!name", in.Name)
		}
		out = append(out, in)
	}
	return out, nil
}
