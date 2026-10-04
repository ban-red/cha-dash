// Package node measures what a long-running chadash-node would hold in memory
// at idle, without Docker: it links the same SDKs as spikes/node-compose,
// initialises the Docker CLI and the Compose service against a socket that
// doesn't exist, versions a stacks root with go-git, and plans one stack
// statically. It never reaches a daemon.
//
//	cd spikes/footprint/node && go test -run TestNodeIdle -v
package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"testing"
	"time"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const stackFile = `name: hello
services:
  web:
    image: nginx:1.27-alpine
    ports: ["8080:80"]
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1/"]
      interval: 10s
      timeout: 3s
      retries: 3
`

type sample struct {
	name       string
	rss        uint64
	footprint  uint64
	heapLive   uint64
	goHeld     uint64
	goroutines int
}

func measure(name string) sample {
	runtime.GC()
	names := []string{
		"/gc/heap/live:bytes",
		"/memory/classes/total:bytes",
		"/memory/classes/heap/released:bytes",
		"/sched/goroutines:goroutines",
	}
	s := make([]metrics.Sample, len(names))
	for i, n := range names {
		s[i].Name = n
	}
	metrics.Read(s)
	fp, _ := footprint()
	return sample{
		name:       name,
		rss:        rss(),
		footprint:  fp,
		heapLive:   s[0].Value.Uint64(),
		goHeld:     s[1].Value.Uint64() - s[2].Value.Uint64(),
		goroutines: int(s[3].Value.Uint64()),
	}
}

func TestNodeIdle(t *testing.T) {
	dir := t.TempDir()
	// Never the real daemon, never the user's Docker config.
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(dir, "missing.sock"))
	t.Setenv("DOCKER_CONFIG", filepath.Join(dir, "docker-config"))
	t.Setenv("DOCKER_CONTEXT", "")

	var rows []sample
	time.Sleep(time.Second)
	rows = append(rows, measure("process (SDKs linked, init done)"))

	cli, err := command.NewDockerCli()
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Initialize(&flags.ClientOptions{}); err != nil {
		t.Fatal(err)
	}
	svc, err := compose.NewComposeService(cli, compose.WithPrompt(compose.AlwaysOkPrompt()))
	if err != nil {
		t.Fatal(err)
	}
	rows = append(rows, measure("+ Docker CLI and Compose service"))

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := svc.List(ctx, api.ListOptions{All: true}); err == nil {
		t.Fatal("listing stacks should fail: there is no daemon")
	}
	rows = append(rows, measure("+ one call (fails: no daemon)"))

	root := filepath.Join(dir, "stacks")
	if err := os.MkdirAll(filepath.Join(root, "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "hello", "compose.yaml")
	if err := os.WriteFile(file, []byte(stackFile), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("hello/compose.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("Adopt hello", &git.CommitOptions{Author: &object.Signature{Name: "footprint", Email: "footprint@chadash.invalid", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	rows = append(rows, measure("+ stacks repo (go-git init, commit)"))

	p, err := svc.LoadProject(ctx, api.ProjectLoadOptions{ProjectName: "hello", ConfigPaths: []string{file}, WorkingDir: filepath.Dir(file)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compose.ServiceHash(p.Services["web"]); err != nil {
		t.Fatal(err)
	}
	rows = append(rows, measure("+ static plan (load, ServiceHash)"))

	time.Sleep(10 * time.Second)
	rows = append(rows, measure("idle 10 s later"))
	debug.FreeOSMemory()
	time.Sleep(time.Second)
	rows = append(rows, measure("after FreeOSMemory"))

	var b strings.Builder
	fmt.Fprintf(&b, "\n%s/%s, %s\n\n| Step | RSS | Footprint | Heap live | Go-held | Goroutines |\n|---|---:|---:|---:|---:|---:|\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %d |\n", r.name, mib(r.rss), mib(r.footprint), mib(r.heapLive), mib(r.goHeld), r.goroutines)
	}
	fmt.Fprintf(&b, "\nMax RSS: %s MiB. Memory in MiB.\n", mib(maxRSS()))
	fmt.Println(b.String())
	runtime.KeepAlive(cli)
}

func mib(b uint64) string { return fmt.Sprintf("%.1f", float64(b)/(1<<20)) }
