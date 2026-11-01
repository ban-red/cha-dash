// Package deploy holds no Go code: install_test.go runs install.sh under a
// POSIX sh with a PATH of fakes, so it never reaches a real Docker, a
// registry or the network.
package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shells are the sh implementations the installer is run under: /bin/sh,
// and dash when it's installed (Debian's /bin/sh, and the strictest).
func shells(t *testing.T) []string {
	out := []string{"/bin/sh"}
	if p, err := exec.LookPath("dash"); err == nil {
		out = append(out, p)
	}
	return out
}

// sandbox is a PATH holding the few real tools install.sh uses (sed, grep,
// awk, head, mkdir, cat) and fakes for the rest. Every fake appends its
// command line to log. docker answers "compose version" and "info" unless
// broken names one of them; curl writes a stand-in compose file.
type sandbox struct {
	t    *testing.T
	path string
	log  string
}

func newSandbox(t *testing.T, fakes map[string]string) *sandbox {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls.log")
	for _, tool := range []string{"sed", "grep", "awk", "head", "mkdir", "cat"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s here", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range fakes {
		script := "#!/bin/sh\nprintf '%s\\n' \"" + name + " $*\" >> '" + log + "'\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &sandbox{t: t, path: bin, log: log}
}

func (s *sandbox) run(shell string, env []string, args ...string) (string, error) {
	s.t.Helper()
	script, err := filepath.Abs("install.sh")
	if err != nil {
		s.t.Fatal(err)
	}
	cmd := exec.Command(shell, append([]string{script}, args...)...)
	cmd.Env = append([]string{"PATH=" + s.path, "HOME=" + s.t.TempDir()}, env...)
	cmd.Dir = s.t.TempDir()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *sandbox) calls() string {
	b, _ := os.ReadFile(s.log)
	return string(b)
}

const (
	dockerOK = `case "$1 $2" in "compose version") echo "Docker Compose version v2.40.0";; esac; exit 0`
	curlOK   = `while [ $# -gt 0 ]; do if [ "$1" = -o ]; then echo "name: chadash" > "$2"; fi; shift; done`
	cosignOK = `exit 0`
	hostOK   = `[ "$1" = -I ] && echo "10.20.30.4 fd00::4"`
)

func TestInstallDryRun(t *testing.T) {
	for _, sh := range shells(t) {
		t.Run(filepath.Base(sh), func(t *testing.T) {
			s := newSandbox(t, map[string]string{"docker": dockerOK, "curl": curlOK, "cosign": cosignOK, "hostname": hostOK})
			out, err := s.run(sh, nil, "--dry-run", "--tag", "v0.4.0", "--dir", "/srv/chadash")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			for _, want := range []string{
				"Installing Cha Dash 0.4.0 into /srv/chadash",
				"+ mkdir -p /srv/chadash",
				// The release's tag, never main.
				"https://raw.githubusercontent.com/ban-red/cha-dash/v0.4.0/deploy/compose.yaml",
				"+ cosign verify ghcr.io/ban-red/chadash:0.4.0 --certificate-identity-regexp ^https://github\\.com/ban-red/cha-dash/\\.github/workflows/release\\.yml@refs/tags/v --certificate-oidc-issuer https://token.actions.githubusercontent.com",
				"+ cosign verify ghcr.io/ban-red/chadash-node:0.4.0",
				"image: ghcr.io/ban-red/chadash:0.4.0",
				"image: ghcr.io/ban-red/chadash-node:0.4.0",
				"+ docker compose --project-directory /srv/chadash up -d",
				"open http://10.20.30.4:3696",
				"Create the owner account now",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("the dry run doesn't say %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "/main/") {
				t.Errorf("fetches from main:\n%s", out)
			}
			// A dry run asks Docker only whether it's there.
			for _, line := range strings.Split(strings.TrimSpace(s.calls()), "\n") {
				if line != "docker compose version" && line != "docker info" && !strings.HasPrefix(line, "hostname") {
					t.Errorf("a dry run ran %q", line)
				}
			}
		})
	}
}

func TestInstallRuns(t *testing.T) {
	for _, sh := range shells(t) {
		t.Run(filepath.Base(sh), func(t *testing.T) {
			s := newSandbox(t, map[string]string{"docker": dockerOK, "curl": curlOK, "cosign": cosignOK})
			dir := filepath.Join(t.TempDir(), "chadash")
			out, err := s.run(sh, []string{"CHADASH_TAG=v0.4.0-beta.1", "CHADASH_DIR=" + dir})
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			calls := s.calls()
			order := []string{
				"docker compose version",
				"docker info",
				"curl -fsSL --proto =https --tlsv1.2 -o " + dir + "/compose.yaml https://raw.githubusercontent.com/ban-red/cha-dash/v0.4.0-beta.1/deploy/compose.yaml",
				"cosign verify ghcr.io/ban-red/chadash:0.4.0-beta.1 ",
				"cosign verify ghcr.io/ban-red/chadash-node:0.4.0-beta.1 ",
				"docker compose --project-directory " + dir + " up -d",
			}
			at := 0
			for _, want := range order {
				i := strings.Index(calls[at:], want)
				if i < 0 {
					t.Fatalf("%q isn't called, or not in order:\n%s", want, calls)
				}
				at += i + len(want)
			}
			b, err := os.ReadFile(filepath.Join(dir, "compose.override.yaml"))
			if err != nil || !strings.Contains(string(b), "image: ghcr.io/ban-red/chadash:0.4.0-beta.1\n") || !strings.HasPrefix(string(b), "# Written by Cha Dash install.sh") {
				t.Fatalf("override: %v\n%s", err, b)
			}
			if !strings.Contains(out, "Both images are signed") || !strings.Contains(out, "<this host>:3696") {
				t.Fatalf("output:\n%s", out)
			}

			// Run again for another release: its own override follows.
			if out, err := s.run(sh, []string{"CHADASH_DIR=" + dir}, "--tag=v0.4.1", "--no-start"); err != nil || !strings.Contains(out, "Not started") {
				t.Fatalf("%v\n%s", err, out)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "compose.override.yaml")); !strings.Contains(string(b), "chadash:0.4.1\n") {
				t.Fatalf("its own override wasn't updated:\n%s", b)
			}
			// One the owner wrote is left alone.
			mine := "services:\n  chadash:\n    environment:\n      TZ: Europe/London\n"
			if err := os.WriteFile(filepath.Join(dir, "compose.override.yaml"), []byte(mine), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err = s.run(sh, []string{"CHADASH_DIR=" + dir}, "--tag", "v0.4.2", "--no-start")
			if b, _ := os.ReadFile(filepath.Join(dir, "compose.override.yaml")); err != nil || string(b) != mine || !strings.Contains(out, "left alone") {
				t.Fatalf("the owner's override: %v %q\n%s", err, b, out)
			}
		})
	}
}

func TestInstallWithoutCosign(t *testing.T) {
	s := newSandbox(t, map[string]string{"docker": dockerOK, "curl": curlOK})
	out, err := s.run("/bin/sh", nil, "--dry-run", "--tag", "v0.4.0")
	if err != nil || !strings.Contains(out, "cosign isn't installed, so the images' signatures weren't checked") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestInstallRefuses(t *testing.T) {
	cases := []struct {
		name  string
		fakes map[string]string
		args  []string
		want  string
	}{
		{"no docker", map[string]string{"curl": curlOK}, []string{"--tag", "v0.4.0"}, "Docker isn't installed"},
		{"no compose plugin", map[string]string{"docker": `[ "$1" = compose ] && exit 1; exit 0`, "curl": curlOK}, []string{"--tag", "v0.4.0"}, "Compose plugin isn't installed"},
		{"docker not answering", map[string]string{"docker": `[ "$1" = info ] && exit 1; exit 0`, "curl": curlOK}, []string{"--tag", "v0.4.0"}, "Docker isn't answering"},
		{"no curl or wget", map[string]string{"docker": dockerOK}, []string{"--tag", "v0.4.0"}, "neither curl nor wget"},
		{"a bad signature", map[string]string{"docker": dockerOK, "curl": curlOK, "cosign": `exit 1`}, []string{"--tag", "v0.4.0"}, "nothing was started"},
		{"not from a release", map[string]string{"docker": dockerOK, "curl": curlOK}, nil, "isn't from a release"},
		{"not a tag", map[string]string{"docker": dockerOK, "curl": curlOK}, []string{"--tag", "main"}, "main isn't a release tag"},
		{"an injected tag", map[string]string{"docker": dockerOK, "curl": curlOK}, []string{"--tag", "v1.0.0;id"}, "isn't a release tag"},
		{"an unknown option", map[string]string{"docker": dockerOK}, []string{"--yes"}, "unknown option --yes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t, c.fakes)
			dir := filepath.Join(t.TempDir(), "chadash")
			out, err := s.run("/bin/sh", []string{"CHADASH_DIR=" + dir}, c.args...)
			if err == nil || !strings.Contains(out, c.want) {
				t.Fatalf("err %v, want %q:\n%s", err, c.want, out)
			}
			if strings.Contains(s.calls(), "up -d") {
				t.Fatal("started anyway")
			}
		})
	}
}

// The release workflow stamps the tag in place of the placeholder, and a
// stamped copy installs that release with no options.
func TestInstallStamped(t *testing.T) {
	src, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(src), "__CHADASH_RELEASE_TAG__") != 1 {
		t.Fatal("install.sh should hold the placeholder exactly once: release.yml replaces it")
	}
	stamped := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(stamped, []byte(strings.Replace(string(src), "__CHADASH_RELEASE_TAG__", "v0.4.0", 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newSandbox(t, map[string]string{"docker": dockerOK, "curl": curlOK})
	cmd := exec.Command("/bin/sh", stamped, "--dry-run")
	cmd.Env = []string{"PATH=" + s.path}
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Installing Cha Dash 0.4.0") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// release.yml attaches install.sh, stamped, and lists it in SHA256SUMS.
func TestReleaseAttachesTheInstaller(t *testing.T) {
	b, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)
	for _, want := range []string{
		`sed "s/__CHADASH_RELEASE_TAG__/$TAG/" ../deploy/install.sh > install.sh`,
		"sha256sum chadash-* install.sh > SHA256SUMS",
		`"chadash-node-$VERSION-linux-armv7" install.sh; do`,
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("release.yml doesn't have %q", want)
		}
	}
}
