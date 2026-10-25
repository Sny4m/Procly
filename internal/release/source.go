// Package release contains the immutable update trust configuration embedded
// in each build. It is not loaded from writable per-user settings.
package release

import (
	"crypto/ed25519"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
)

//go:embed source.json
var files embed.FS

type Source struct {
	Owner      string `json:"owner"`
	Repository string `json:"repository"`
	PublicKey  string `json:"signing_public_key"`
}

func Official() (Source, error) {
	var s Source
	b, e := files.ReadFile("source.json")
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, e
	}
	valid := regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	if !valid.MatchString(s.Owner) || !valid.MatchString(s.Repository) || s.PublicKey == "" {
		return s, fmt.Errorf("official releases are not configured in this build; set internal/release/source.json before publishing")
	}
	key, err := base64.StdEncoding.DecodeString(s.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return s, fmt.Errorf("release signing public key is invalid in this build")
	}
	return s, nil
}
