// Command chadash-node is Cha Dash's node: it runs on a Docker host, dials
// out to the hub, and does the Docker work the hub asks for, within the
// host owner's local policy (/etc/chadash-node/policy.yaml).
//
//	chadash-node serve         connect to the hub and serve its requests
//	chadash-node healthcheck   exit 0 if the node is running (for Docker HEALTHCHECK)
//	chadash-node keygen        make the node's key if there isn't one, and print its pin
//	chadash-node show-key      print the node's pin and fingerprint
//	chadash-node version       print the build version
//
// Settings come from CHADASH_NODE_* environment variables (see config.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/node/docker"
	"dash.cha.sh/internal/node/stacks"
	"dash.cha.sh/internal/nodeproto"
)

func main() {
	privateUmask()
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	cfg, err := loadConfig(getenv)
	switch args[0] {
	case "version", "--version", "-version":
		fmt.Fprintln(stdout, "chadash-node "+strings.TrimPrefix(buildinfo.String(), "chadash "))
		return 0
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "chadash-node: %v\n", err)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(ctx, cfg, stderr)
	case "healthcheck":
		r, err := node.CheckHealth(cfg.HealthFile, time.Now())
		if err != nil {
			fmt.Fprintf(stderr, "chadash-node healthcheck: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s %s\n", r.State, r.Detail)
		return 0
	case "keygen", "show-key":
		return showKey(cfg, args[0] == "keygen", stdout, stderr)
	}
	fmt.Fprintf(stderr, "chadash-node: unknown command %q\n\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: chadash-node <command>

Commands:
  serve        connect to the hub and serve its requests, within the local policy
  healthcheck  exit 0 if the node is running, 1 if not
  keygen       make the node's key if there isn't one, and print its pin
  show-key     print the node's pin and fingerprint (never the private key)
  version      print the build version

Settings are CHADASH_NODE_* environment variables: HUB, NAME, DIR, POLICY,
PAIRING, PAIRING_FILE, DOCKER_HOST, HEALTH_FILE, LOG_LEVEL, LOG_FORMAT.
See deploy/README.md.
`)
}

func showKey(cfg config, create bool, stdout, stderr io.Writer) int {
	path := cfg.identityDir()
	if !create {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "chadash-node: no identity in %s yet (chadash-node keygen makes one)\n", path)
			return 1
		}
	}
	id, err := node.OpenIdentity(path)
	if err != nil {
		fmt.Fprintf(stderr, "chadash-node: %v\n", err)
		return 1
	}
	pin := id.Current().Pin
	fmt.Fprintf(stdout, "pin:         %s\nfingerprint: %s\n", pin, nodeproto.Fingerprint(pin))
	if st := id.State(); st.Node != "" {
		fmt.Fprintf(stdout, "node:        %s\n", st.Node)
	}
	return 0
}

func serve(ctx context.Context, cfg config, stderr io.Writer) int {
	log := cfg.logger(stderr)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runNode(ctx, cfg, log); err != nil {
		log.Error("node stopped", "err", err)
		return 1
	}
	log.Info("node stopped")
	return 0
}

func runNode(ctx context.Context, cfg config, log *slog.Logger) error {
	log.Info("starting "+strings.Replace(buildinfo.String(), "chadash", "chadash-node", 1), "dir", cfg.Dir, "docker", cfg.DockerHost)
	policy, missing, err := node.LoadPolicy(cfg.PolicyPath)
	if err != nil {
		return err
	}
	grants := []any{"path", cfg.PolicyPath, "docker", strings.Join(policy.Docker.Allow, ","), "stacks", strings.Join(policy.Stacks.Allow, ",")}
	if missing {
		log.Info("no policy file; using the default policy", grants...)
	} else {
		// Say what the file grants, so a mounted policy can be checked from the logs.
		log.Info("policy loaded", append(grants, "stacks_deny", strings.Join(policy.Stacks.Deny, ","), "protect", strings.Join(policy.Docker.Protect, ","))...)
	}
	hubURL := cfg.HubURL
	if policy.Hub.URL != "" {
		hubURL = policy.Hub.URL // the owner's file wins over the environment
	}
	ident, err := node.OpenIdentity(cfg.identityDir())
	if err != nil {
		return err
	}
	dm, err := docker.New(docker.Options{Host: cfg.DockerHost, Policy: policy, Log: log.With("module", "docker")})
	if err != nil {
		return err
	}
	defer dm.Close()
	// Owned stacks: the stacks root under go-git and the Compose SDK, on the
	// same daemon. The node's own compose project is view-only.
	sdk, err := stacks.NewSDK(cfg.DockerHost)
	if err != nil {
		return err
	}
	// The node's own project is asked for lazily, and again until Docker
	// answers, so a slow daemon at start doesn't leave it deployable.
	sm, err := stacks.New(stacks.Options{
		Root: policy.Stacks.Root, Docker: sdk, SelfProject: dm.SelfProject,
		ProtectContainer: policy.Protected,
		Guard:            stackGuard(ctx, cfg, policy, dm, log),
		PrivateDirs:      []string{cfg.Dir, cfg.identityDir(), filepath.Dir(cfg.PairingFile)},
		Log:              log.With("module", "stacks"),
		// Git sync: the shallow caches live in the node's own directory,
		// never under the stacks root, and reach only stacks.remotes.
		CacheDir: filepath.Join(cfg.Dir, "git-cache"),
		Remotes:  policy.Stacks.Remotes,
	})
	if err != nil {
		return err
	}
	disp, err := node.NewDispatcher(policy, dm, sm)
	if err != nil {
		return err
	}
	health := node.NewHealth(cfg.HealthFile)
	go health.Run(ctx)

	pairing, err := waitForPairing(ctx, cfg, ident, policy, log)
	if err != nil {
		return err
	}
	// The secret is single-use and the hub regenerates it, so a token that
	// came from the file is read again whenever the node pairs.
	pairingFile := ""
	if cfg.Pairing == "" {
		pairingFile = cfg.PairingFile
	}
	c, err := node.NewClient(node.Config{
		HubURL: hubURL, Name: cfg.Name, Build: buildinfo.Version, Identity: ident, Dispatcher: disp,
		Pairing: pairing, PairingFile: pairingFile, Log: log, Health: health,
	})
	if err != nil {
		return err
	}
	return c.Run(ctx)
}

// stackGuard is what no stack may touch on this host: the node's own
// directories, the pairing file's, and the Docker socket, both as the node
// sees them and, when it runs in a container, as the host does (its
// mounts' sources and volume names). The policy's stacks.deny and
// bind_roots narrow the rest.
func stackGuard(ctx context.Context, cfg config, policy node.Policy, dm *docker.Module, log *slog.Logger) stacks.Guard {
	g := stacks.Guard{Deny: policy.Stacks.Deny, BindRoots: policy.Stacks.BindRoots}
	local := []string{cfg.Dir, cfg.identityDir(), filepath.Dir(cfg.PairingFile)}
	if sock, ok := strings.CutPrefix(cfg.DockerHost, "unix://"); ok {
		local = append(local, sock)
	}
	g.PrivatePaths = append(g.PrivatePaths, local...)
	mctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	mounts, err := dm.SelfMounts(mctx)
	if err != nil {
		log.Warn("couldn't read the node's own mounts; stacks are kept off its usual paths only", "err", err)
	}
	for _, mt := range mounts {
		if !slices.ContainsFunc(local, func(p string) bool { return within(mt.Destination, p) || within(p, mt.Destination) }) {
			continue
		}
		if mt.Source != "" {
			g.PrivatePaths = append(g.PrivatePaths, mt.Source)
		}
		if mt.Name != "" {
			g.PrivateVolumes = append(g.PrivateVolumes, mt.Name)
		}
	}
	return g
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// waitForPairing returns the pairing token: CHADASH_NODE_PAIRING, or the
// file the hub writes into the shared volume. A node that has never met
// its hub and has no hub pin waits for the file: the hub may still be
// starting. Once enrolled, the token is optional.
func waitForPairing(ctx context.Context, cfg config, ident *node.IdentityStore, policy node.Policy, log *slog.Logger) (string, error) {
	if cfg.Pairing != "" {
		return cfg.Pairing, nil
	}
	read := func() string {
		b, err := os.ReadFile(cfg.PairingFile)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	if tok := read(); tok != "" {
		return tok, nil
	}
	st := ident.State()
	if st.Node != "" && (len(st.HubPins) > 0 || policy.Hub.Pin != "") {
		return "", nil
	}
	log.Info("waiting for the hub's pairing token", "file", cfg.PairingFile)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-t.C:
			if tok := read(); tok != "" {
				return tok, nil
			}
		}
	}
}
