//go:build engine

package deploy

import (
	"os"
	"strings"
	"testing"

	"dash.cha.sh/internal/testengine"
)

// The Proxmox LXC scripts (lxc/), run unchanged in a throwaway Debian 13
// container, as root, the way the LXC runs them (H7 in
// docs/plan/17-test-automation.md). Nothing leaves the container: curl is
// a fake that serves a stand-in for community-scripts' build.func (never
// the real one), GitHub's release answer and the release's install.sh; apt-get,
// jq, dpkg and docker are fakes too. Every fake appends its command line to
// /tmp/calls.log, which the test reads.

const lxcRelease = "v0.9.0"

// buildFunc stands in for community-scripts' build.func: its functions as
// no-ops that say they ran. start runs update_script when the test asks, as
// build.func does inside an existing container.
const buildFunc = `
header_info() { :; }
variables() { :; }
color() { :; }
catch_errors() { :; }
check_container_storage() { :; }
check_container_resources() { :; }
msg_info() { echo "INFO: $*"; }
msg_ok() { echo "OK: $*"; }
msg_error() { echo "ERROR: $*"; }
STD=""
IP=10.0.11.50
start() { if [ -n "$TEST_UPDATE" ]; then update_script; fi; }
build_container() { echo "build_container os=$var_os version=$var_version unprivileged=$var_unprivileged cpu=$var_cpu ram=$var_ram disk=$var_disk tags=$var_tags" >> /tmp/calls.log; }
description() { :; }
`

// installFunctions stands in for FUNCTIONS_FILE_PATH, what build.func hands
// the install script inside the new container.
const installFunctions = `
color() { :; }
verb_ip6() { :; }
catch_errors() { :; }
setting_up_container() { :; }
network_check() { :; }
update_os() { :; }
motd_ssh() { :; }
customize() { :; }
cleanup_lxc() { :; }
msg_info() { echo "INFO: $*"; }
msg_ok() { echo "OK: $*"; }
msg_error() { echo "ERROR: $*"; }
STD=""
`

func fake(body string) []byte {
	return []byte("#!/bin/sh\nprintf '%s\\n' \"$(basename \"$0\") $*\" >> /tmp/calls.log\n" + body + "\n")
}

// lxcFiles are the scripts and the fakes, at the paths the container sees.
func lxcFiles(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, f := range []string{"lxc/ct/chadash.sh", "lxc/install/chadash-install.sh"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		files["/lxc/"+strings.TrimPrefix(f, "lxc/")] = b
	}
	files["/fake/build.func"] = []byte(buildFunc)
	files["/fake/functions"] = []byte(installFunctions)
	// The release's install.sh: says how it was called.
	files["/fake/install.sh"] = []byte("printf 'install.sh %s\\n' \"$*\" >> /tmp/calls.log\n")
	files["/fake/bin/curl"] = fake(`out=""; url=""
while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift;; -*) ;; *) url="$1";; esac; shift; done
case "$url" in
  *misc/build.func) cat /fake/build.func;;
  *api.github.com/repos/ban-red/cha-dash/releases/latest) printf '{"tag_name":"%s","name":"Cha Dash"}\n' "${TEST_TAG:-` + lxcRelease + `}";;
  *releases/download/*/install.sh) cp /fake/install.sh "$out";;
  *download.docker.com/linux/debian/gpg) echo "fake key" > "$out";;
  *) echo "curl: unexpected $url" >&2; exit 22;;
esac`)
	files["/fake/bin/jq"] = fake(`sed -n 's/.*"tag_name":"\([^"]*\)".*/\1/p'`)
	files["/fake/bin/apt-get"] = fake(`exit 0`)
	files["/fake/bin/docker"] = fake(`printf 'docker in %s\n' "$PWD" >> /tmp/calls.log`)
	files["/fake/bin/dpkg"] = fake(`[ "$1" = --print-architecture ] && echo amd64`)
	return files
}

// lxcRun runs script under bash in a fresh container and returns its
// output, its exit code and the calls the fakes logged.
func lxcRun(t *testing.T, e *testengine.Engine, name, setup, script string) (out string, code int, calls string) {
	t.Helper()
	cmd := `export PATH=/fake/bin:$PATH FUNCTIONS_FILE_PATH="$(cat /fake/functions)"; ` + setup + `
bash ` + script + `; echo "EXIT=$?"; echo "--- calls"; cat /tmp/calls.log 2>/dev/null; echo "--- files"
ls /tmp/chadash-install.sh 2>/dev/null; cat /etc/apt/sources.list.d/docker.list 2>/dev/null`
	got, _ := e.RunOnce(name, testengine.Debian, lxcFiles(t), "bash", "-c", cmd)
	head, rest, _ := strings.Cut(got, "--- calls\n")
	calls, _, _ = strings.Cut(rest, "--- files\n")
	_, files, _ := strings.Cut(rest, "--- files\n")
	i := strings.LastIndex(head, "EXIT=")
	if i < 0 {
		t.Fatalf("%s didn't finish:\n%s", name, got)
	}
	code = 0
	for _, c := range strings.TrimSpace(head[i+5:]) {
		code = code*10 + int(c-'0')
	}
	return head[:i] + files, code, calls
}

func TestEngineLXCInstall(t *testing.T) {
	e := testengine.New(t)
	out, code, calls := lxcRun(t, e, "lxc-install", "", "/lxc/install/chadash-install.sh")
	if code != 0 {
		t.Fatalf("install exited %d:\n%s\n%s", code, out, calls)
	}
	for _, want := range []string{
		"apt-get install -y ca-certificates curl jq",
		"apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin",
		"curl -fsSL https://api.github.com/repos/ban-red/cha-dash/releases/latest",
		"curl -fsSL https://github.com/ban-red/cha-dash/releases/download/" + lxcRelease + "/install.sh -o /tmp/chadash-install.sh",
		"install.sh --dir /opt/chadash --tag " + lxcRelease,
	} {
		if !strings.Contains(calls, want+"\n") {
			t.Errorf("no %q in the calls:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "--no-start") {
		t.Error("a new install starts the hub")
	}
	if !strings.Contains(out, "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable") {
		t.Errorf("Docker's apt source for Debian 13:\n%s", out)
	}
	if strings.Contains(out, "/tmp/chadash-install.sh") {
		t.Error("the downloaded installer was left in /tmp")
	}
}

// A release tag that isn't vX.Y.Z (an error page, null, something else) is
// refused before anything is fetched with it.
func TestEngineLXCRefusesABadRelease(t *testing.T) {
	e := testengine.New(t)
	for i, tag := range []string{"null", "v1.2", "v1.2.3; rm -rf /", "v1.2.3-beta.1"} {
		for _, s := range []struct{ name, setup, script string }{
			{"install", "", "/lxc/install/chadash-install.sh"},
			{"update", "mkdir -p /opt/chadash && touch /opt/chadash/compose.yaml; export TEST_UPDATE=1;", "/lxc/ct/chadash.sh"},
		} {
			out, code, calls := lxcRun(t, e, "lxc-bad-"+s.name+"-"+string(rune('a'+i)), "export TEST_TAG='"+tag+"'; "+s.setup, s.script)
			if s.name == "install" && code == 0 {
				t.Errorf("%s with tag %q exited 0:\n%s", s.name, tag, out)
			}
			if !strings.Contains(out, "ERROR: Couldn't read the latest release") || strings.Contains(calls, "install.sh") {
				t.Errorf("%s with tag %q:\n%s\n%s", s.name, tag, out, calls)
			}
		}
	}
}

func TestEngineLXCContainer(t *testing.T) {
	e := testengine.New(t)

	// On the Proxmox host: a Debian 13 LXC, unprivileged, with the defaults.
	out, code, calls := lxcRun(t, e, "lxc-ct", "", "/lxc/ct/chadash.sh")
	if code != 0 || !strings.Contains(calls, "build_container os=debian version=13 unprivileged=1 cpu=1 ram=1024 disk=6 tags=dashboard;homelab;docker\n") {
		t.Fatalf("ct exited %d:\n%s\n%s", code, out, calls)
	}
	if !strings.Contains(out, "http://10.0.11.50:3696") || !strings.Contains(out, "create the owner account") {
		t.Errorf("the closing message:\n%s", out)
	}
	if strings.Contains(calls, "install.sh") {
		t.Error("making the container ran an update")
	}

	// Update, inside an LXC with no install: refused.
	out, _, calls = lxcRun(t, e, "lxc-update-none", "export TEST_UPDATE=1;", "/lxc/ct/chadash.sh")
	if !strings.Contains(out, "ERROR: No Cha Dash Installation Found!") || strings.Contains(calls, "install.sh") {
		t.Fatalf("update with no install:\n%s\n%s", out, calls)
	}

	// Update of an install: the newest release's installer in the same
	// folder, without starting, then pull and recreate there.
	out, code, calls = lxcRun(t, e, "lxc-update", "mkdir -p /opt/chadash && touch /opt/chadash/compose.yaml; export TEST_UPDATE=1;", "/lxc/ct/chadash.sh")
	want := strings.Join([]string{
		"curl -fsSL https://api.github.com/repos/ban-red/cha-dash/releases/latest",
		"jq -r .tag_name",
		"curl -fsSL https://github.com/ban-red/cha-dash/releases/download/" + lxcRelease + "/install.sh -o /tmp/chadash-install.sh",
		"install.sh --dir /opt/chadash --tag " + lxcRelease + " --no-start",
		"docker compose pull",
		"docker in /opt/chadash",
		"docker compose up -d",
		"docker in /opt/chadash",
	}, "\n") + "\n"
	if code != 0 || !strings.HasSuffix(calls, want) {
		t.Fatalf("update exited %d, calls:\n%s\nwant them to end with:\n%s\n%s", code, calls, want, out)
	}
	if strings.Contains(out, "/tmp/chadash-install.sh") {
		t.Error("the downloaded installer was left in /tmp")
	}
	if !strings.Contains(out, "OK: Updated Cha Dash to "+lxcRelease) {
		t.Errorf("update output:\n%s", out)
	}
}
