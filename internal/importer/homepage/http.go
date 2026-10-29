package homepage

import (
	"path"
	"strings"

	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/importer/importhttp"
)

// MaxBodyBytes bounds an import request: the three files together.
const MaxBodyBytes = 1 << 20

// ImportSource is the Homepage importer for the shared import API
// (importhttp): POST /api/import/homepage takes {files: {services_yaml,
// docker_yaml, settings_yaml}}, or the three at the top level as before.
// An applied import is audited as import.homepage.
func ImportSource() importhttp.Source {
	return importhttp.Source{
		ID: Source, Name: "Homepage", MaxBytes: MaxBodyBytes,
		Roles: []importhttp.Role{
			{Key: "services_yaml", File: servicesFile, Required: true},
			{Key: "docker_yaml", File: dockerFile},
			{Key: "settings_yaml", File: settingsFile},
		},
		Detect: detect,
		Build: func(files map[string][]byte) (*importer.Result, importer.Secrets, error) {
			return Build(Input{Services: string(files["services_yaml"]), Docker: string(files["docker_yaml"]), Settings: string(files["settings_yaml"])})
		},
	}
}

// detect knows Homepage's files by name, and a pasted or renamed
// services.yaml by its shape: a list of groups whose entries carry href,
// icon or widget.
func detect(name string, head []byte) (string, bool) {
	base := strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/")))
	stem, ext := strings.TrimSuffix(base, path.Ext(base)), path.Ext(base)
	if ext != ".yaml" && ext != ".yml" {
		return "", false
	}
	switch stem {
	case "services":
		return "services_yaml", true
	case "docker":
		return "docker_yaml", true
	case "settings":
		return "settings_yaml", true
	}
	text := string(head)
	if strings.HasPrefix(strings.TrimSpace(text), "- ") && (strings.Contains(text, "href:") || strings.Contains(text, "widget:")) {
		return "services_yaml", true
	}
	return "", false
}
