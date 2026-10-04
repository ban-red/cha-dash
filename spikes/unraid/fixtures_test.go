package main

import (
	"strings"
	"testing"
)

const dockerFixtureDir = "../../testdata/integrations/docker/jupiter"

// The Unraid API and the Docker socket proxy describe the same 74 containers. The fixtures
// keep their ids aligned, so the hub's correlator can join on the container id (exact key)
// and fall back to the name.
func TestContainersJoinWithDockerFixture(t *testing.T) {
	rows, byID, byName, err := joinContainers(fixturesDir, dockerFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 74 || byID != 74 || byName != 74 {
		t.Fatalf("join: %d containers, %d by id, %d by name; want 74 each", len(rows), byID, byName)
	}
	for _, r := range rows {
		if r.UnraidID != r.DockerID {
			t.Errorf("%s: Unraid id and Docker id differ", r.Name)
		}
		// The API folds Docker's "created" into EXITED; everything else must agree.
		if !strings.EqualFold(r.UnraidState, r.DockerState) && !(r.UnraidState == "EXITED" && r.DockerState == "created") {
			t.Errorf("%s: state %s vs %s", r.Name, r.UnraidState, r.DockerState)
		}
	}
}

// Desired state: autostart for Unraid template containers, restart policy for the rest.
func TestDesiredState(t *testing.T) {
	rows, _, _, err := joinContainers(fixturesDir, dockerFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	var restartNo, autostart, composeAutostart, autostartNo int
	verdicts := map[string]int{}
	for _, r := range rows {
		if r.Restart == "no" {
			restartNo++
		}
		if r.AutoStart {
			autostart++
			if r.Manager != "dockerman" {
				composeAutostart++
			}
			if r.Restart == "no" {
				autostartNo++
			}
		}
		if r.Verdict == "" || r.Desired == "" {
			t.Errorf("%s: no verdict", r.Name)
		}
		verdicts[r.Verdict]++
	}
	if restartNo != 60 {
		t.Errorf("%d containers with restart policy no, want 60 (spike 4.4)", restartNo)
	}
	// Autostart is a template (dockerman) concept: Compose Manager stacks are never on the list.
	if autostart != 36 || composeAutostart != 0 {
		t.Errorf("autostart: %d containers (%d not templates); want 36 and 0", autostart, composeAutostart)
	}
	// Most autostart containers rely on Unraid alone: Docker would never restart them.
	if autostartNo != 30 {
		t.Errorf("%d autostart containers with restart policy no, want 30", autostartNo)
	}
	want := map[string]int{"ok": 42, "stopped-on-purpose": 30, "no-autostart": 2}
	for k, n := range want {
		if verdicts[k] != n {
			t.Errorf("verdict %s: %d, want %d (all: %v)", k, verdicts[k], n, verdicts)
		}
	}
}

// The disks read keys each disk by its bare serial (id "<server id>:<serial>"); the array keys
// it by "<model>_<serial>" (the flash: "<vendor>_<model>_<serial>-0:0", unprefixed). Both
// fixtures carry the same fake serials, so they join on the serial, and on the device too.
func TestDisksJoinArrayBySerial(t *testing.T) {
	var arr struct {
		Data struct {
			Array struct {
				Boot                    arrayDevice
				Parities, Disks, Caches []arrayDevice
			}
		}
	}
	var dsk struct {
		Data struct {
			Disks []struct {
				ID, Device, SerialNum string
				IsSpinning            bool
			}
		}
	}
	for path, v := range map[string]any{"array.json": &arr, "disks.json": &dsk} {
		if err := readJSON(fixturesDir+"/jupiter/"+path, v); err != nil {
			t.Fatal(err)
		}
	}

	a := arr.Data.Array
	bySerial := map[string]arrayDevice{}
	for _, d := range append(append(append([]arrayDevice{a.Boot}, a.Parities...), a.Disks...), a.Caches...) {
		s := diskSerial(localID(d.ID))
		if _, dup := bySerial[s]; dup || s == "" {
			t.Fatalf("%s: serial empty or not unique", d.Name)
		}
		bySerial[s] = d
	}
	if len(bySerial) != 16 || len(dsk.Data.Disks) != 16 {
		t.Fatalf("%d array devices, %d disks; want 16 each", len(bySerial), len(dsk.Data.Disks))
	}
	for _, d := range dsk.Data.Disks {
		if !serverPrefix.MatchString(d.ID) || localID(d.ID) != d.SerialNum {
			t.Errorf("%s: disk id isn't <server id>:<serialNum>", d.Device)
		}
		ad, ok := bySerial[d.SerialNum]
		if !ok {
			t.Errorf("%s: serial matches no array device", d.Device)
			continue
		}
		if d.Device != "/dev/"+ad.Device {
			t.Errorf("%s: joined %s by serial, whose device is %s", d.Device, ad.Name, ad.Device)
		}
		// API 4.37.2 matches emhttp's "<model>_<serial>" id against the bare serial, which
		// never matches, so isSpinning is always false here (every disk was spinning).
		if d.IsSpinning {
			t.Errorf("%s: isSpinning true; the 4.37.2 bug is gone, update the README", d.Device)
		}
	}
	// The boot flash: unprefixed in the array, prefixed in disks, same all-digit serial.
	if serverPrefix.MatchString(a.Boot.ID) || !strings.HasSuffix(a.Boot.ID, "-0:0") {
		t.Errorf("boot flash id shape changed")
	}
}

type arrayDevice struct{ ID, Name, Device string }

func TestLocalID(t *testing.T) {
	prefix := strings.Repeat("ab", 32) + ":"
	for in, want := range map[string]string{
		prefix + "disk1": "disk1",
		prefix + "WDC_WD140EDGZ-11B1PA0_9LJABCDE":     "WDC_WD140EDGZ-11B1PA0_9LJABCDE",
		"SanDisk_Cruzer_Fit_4C530001230123456789-0:0": "SanDisk_Cruzer_Fit_4C530001230123456789-0:0", // no server prefix
	} {
		if got := localID(in); got != want {
			t.Errorf("localID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiskSerial(t *testing.T) {
	for in, want := range map[string]string{
		"WDC_WD140EDGZ-11B1PA0_9LJABCDE":              "9LJABCDE",
		"Samsung_SSD_980_PRO_1TB_S5GXNF0R123456K":     "S5GXNF0R123456K",
		"SanDisk_Cruzer_Fit_4C530001230123456789-0:0": "4C530001230123456789",
		"Samsung_Flash_Drive_0123456789012345-0:0":    "0123456789012345", // all digits, like jupiter's
		"TEAM_T2532TB_ABCD0123456789012345":           "ABCD0123456789012345",
		"nvme0n1":                                     "nvme0n1",
	} {
		if got := diskSerial(in); got != want {
			t.Errorf("diskSerial(%q) = %q, want %q", in, got, want)
		}
	}
}
