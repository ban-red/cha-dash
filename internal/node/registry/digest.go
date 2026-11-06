package registry

import (
	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Digest is the digest a registry serves an image under for the
// repository name ("127.0.0.1:5000/app", "docker.io/library/nginx"), or ""
// when the engine can't tell: first from the image's RepoDigests, then,
// on the containerd image store (where Docker sets desc, the image's OCI
// descriptor), from the image itself. That store keeps no RepoDigests for
// an image once its tag has moved to another one, but the image's own
// digest is the manifest, or index, the registry served or was pushed:
// the same digest the registry knows it by. The classic store sets no
// descriptor, and an image there with no RepoDigests for name was built
// or loaded on the host.
func Digest(repoDigests []string, desc *ocispec.Descriptor, name string) string {
	want, err := reference.ParseNormalizedNamed(name)
	if err != nil {
		return ""
	}
	for _, rd := range repoDigests {
		if d, err := reference.ParseNormalizedNamed(rd); err == nil && d.Name() == want.Name() {
			if dg, ok := d.(reference.Digested); ok {
				return dg.Digest().String()
			}
		}
	}
	if desc != nil && desc.Digest != "" {
		return desc.Digest.String()
	}
	return ""
}
