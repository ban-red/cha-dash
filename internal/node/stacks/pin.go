package stacks

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// pinImages rewrites each service's image in content to the digest it last
// ran, when that digest is for the same image: "nginx:1.27" becomes
// "nginx:1.27@sha256:…". Only the scalar on the image line changes, so the
// rest of the file (comments, order, indentation) stays as written.
// refs maps a service to a digest reference ("nginx@sha256:…"). It returns
// the new content and the services it pinned.
func pinImages(content string, refs map[string]string) (string, []string) {
	if len(refs) == 0 {
		return content, nil
	}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(content), &doc) != nil {
		return content, nil
	}
	svcs := mapValue(&doc, "services")
	if svcs == nil || svcs.Kind != yaml.MappingNode {
		return content, nil
	}
	lines := strings.SplitAfter(content, "\n")
	var pinned []string
	for i := 0; i+1 < len(svcs.Content); i += 2 {
		name, svc := svcs.Content[i].Value, svcs.Content[i+1]
		ref, ok := refs[name]
		if !ok || svc.Kind != yaml.MappingNode {
			continue
		}
		img := childValue(svc, "image")
		if img == nil || img.Kind != yaml.ScalarNode || strings.Contains(img.Value, "@") || strings.Contains(img.Value, "$") {
			continue
		}
		_, digest, ok := strings.Cut(ref, "@")
		if !ok || !strings.HasPrefix(digest, "sha256:") || !sameRepo(img.Value, ref) {
			continue
		}
		ln := img.Line - 1
		if ln < 0 || ln >= len(lines) {
			continue
		}
		line := lines[ln]
		col := img.Column - 1
		if col < 0 || col > len(line) {
			continue
		}
		rest := line[col:]
		old := img.Value
		var replaced string
		switch {
		case strings.HasPrefix(rest, old):
			replaced = line[:col] + old + "@" + digest + rest[len(old):]
		case strings.HasPrefix(rest, `"`+old+`"`), strings.HasPrefix(rest, `'`+old+`'`):
			q := rest[:1]
			replaced = line[:col] + q + old + "@" + digest + q + rest[len(old)+2:]
		default:
			continue
		}
		lines[ln] = replaced
		pinned = append(pinned, name)
	}
	return strings.Join(lines, ""), pinned
}

func childValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// sameRepo reports whether image ("nginx:1.27", "ghcr.io/x/y:2") and a
// digest reference ("nginx@sha256:…", "docker.io/library/nginx@sha256:…")
// name the same repository.
func sameRepo(image, ref string) bool {
	repo, _, _ := strings.Cut(ref, "@")
	return normalRepo(stripTag(image)) == normalRepo(repo)
}

func stripTag(image string) string {
	if i := strings.LastIndexByte(image, ':'); i > strings.LastIndexByte(image, '/') {
		return image[:i]
	}
	return image
}

// normalRepo expands Docker Hub's short names: nginx → docker.io/library/nginx.
func normalRepo(r string) string {
	if rest, ok := strings.CutPrefix(r, "index.docker.io/"); ok {
		r = "docker.io/" + rest
	}
	first, _, hasSlash := strings.Cut(r, "/")
	if !hasSlash {
		return "docker.io/library/" + r
	}
	if first == "docker.io" && strings.Count(r, "/") == 1 {
		return "docker.io/library/" + strings.TrimPrefix(r, "docker.io/")
	}
	if !strings.ContainsAny(first, ".:") && first != "localhost" {
		return "docker.io/" + r
	}
	return r
}
