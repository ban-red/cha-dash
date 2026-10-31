package integration

import "testing"

// UseURL keeps the address and drops everything that could carry a
// credential.
func TestUseURL(t *testing.T) {
	for _, c := range []struct {
		scheme, host string
		port         int
		path, want   string
	}{
		{"http", "10.0.11.120", 8080, "", "http://10.0.11.120:8080"},
		{"HTTPS", "qbit.lab.example", 0, "/base/", "https://qbit.lab.example/base"},
		{"mqtt", "fd00::5", 1883, "", "mqtt://[fd00::5]:1883"},
		{"http", "[fd00::5]", 80, "x", "http://[fd00::5]:80/x"},
		{"http", "user:pass@host.example", 80, "", ""},
		{"http", "", 80, "", ""},
		{"", "host.example", 80, "", ""},
		{"http", "host.example", 70000, "/a?apikey=x#y", "http://host.example/a"},
	} {
		if got := UseURL(c.scheme, c.host, c.port, c.path); got != c.want {
			t.Errorf("UseURL(%q, %q, %d, %q) = %q, want %q", c.scheme, c.host, c.port, c.path, got, c.want)
		}
	}
}
