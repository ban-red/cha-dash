package main

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// The guard is an allowlist: the spike may only send GET/HEAD (enforced by the
// labkit client) to these read endpoints. Everything else is refused before
// it reaches the network, including exec, attach, archive, export and logs
// (except the zero-stream logs probe, which can never return log content).
var allowed = []*regexp.Regexp{
	re(`/_ping`),
	re(`/(version|info|events)`),
	re(`/containers/json`),
	re(`/containers/[A-Za-z0-9][A-Za-z0-9_.-]*/(json|stats)`),
	re(`/containers/[A-Za-z0-9][A-Za-z0-9_.-]*/logs`), // zero-stream only, see below
	re(`/images/json`),
	re(`/images/[A-Za-z0-9][A-Za-z0-9_.:/@-]*/json`),
	re(`/networks(/[A-Za-z0-9][A-Za-z0-9_.-]*)?`),
	re(`/volumes(/[A-Za-z0-9][A-Za-z0-9_.-]*)?`),
	re(`/system/df`),
	re(`/(secrets|configs|swarm|plugins|services|tasks|nodes)`),
}

func re(p string) *regexp.Regexp { return regexp.MustCompile(`^(/v1\.[0-9]+)?` + p + `$`) }

var logsPath = regexp.MustCompile(`/containers/[^/]+/logs$`)

func dockerGuard(r *http.Request) error {
	p := r.URL.Path
	ok := false
	for _, a := range allowed {
		if a.MatchString(p) {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("path not on the read allowlist")
	}
	q := r.URL.Query()
	switch {
	case logsPath.MatchString(p):
		// Docker answers 400 "you must choose at least one stream" before it
		// looks at the container, so this form can't return log content. A
		// filtering proxy answers with its own refusal instead.
		if q.Get("stdout") != "0" || q.Get("stderr") != "0" || q.Get("follow") == "1" || q.Get("follow") == "true" {
			return errors.New("logs may only be probed with stdout=0&stderr=0")
		}
	case strings.HasSuffix(p, "/events"):
		if q.Get("until") == "" || q.Get("since") == "" {
			return errors.New("events needs a since/until window")
		}
	case strings.HasSuffix(p, "/stats"):
		if q.Get("stream") != "false" {
			return errors.New("stats must be one-shot (stream=false)")
		}
	}
	return nil
}
