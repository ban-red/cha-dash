package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argonParams are argon2id cost settings. They're stored in each hash (PHC
// string format), so they can be raised later without invalidating old hashes.
type argonParams struct {
	time    uint32
	memory  uint32 // KiB
	threads uint8
	keyLen  uint32
	saltLen int
}

// defaultParams follow OWASP's argon2id baseline (19 MiB, t=2, p=1). That keeps
// a login well inside the hub's memory budget on small arm boards; hashSem
// bounds how many run at once.
var defaultParams = argonParams{time: 2, memory: 19 * 1024, threads: 1, keyLen: 32, saltLen: 16}

// hashSem caps concurrent hashes so a burst of logins can't balloon memory.
var hashSem = make(chan struct{}, 2)

var errBadHash = errors.New("auth: malformed password hash")

func hashPassword(password string, p argonParams) string {
	salt := make([]byte, p.saltLen)
	rand.Read(salt)
	hashSem <- struct{}{}
	key := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	<-hashSem
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

func verifyPassword(password, encoded string) (bool, error) {
	p, salt, want, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	hashSem <- struct{}{}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(want)))
	<-hashSem
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseHash(encoded string) (p argonParams, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, errBadHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return p, nil, nil, errBadHash
	}
	// Sanity bounds so a corrupted row can't make verification allocate gigabytes.
	if p.memory < 8 || p.memory > 1<<20 || p.time < 1 || p.time > 16 || p.threads < 1 {
		return p, nil, nil, errBadHash
	}
	b64 := base64.RawStdEncoding
	if salt, err = b64.DecodeString(parts[4]); err != nil || len(salt) < 8 {
		return p, nil, nil, errBadHash
	}
	if key, err = b64.DecodeString(parts[5]); err != nil || len(key) < 16 {
		return p, nil, nil, errBadHash
	}
	return p, salt, key, nil
}
