package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"github.com/procly/procly/internal/release"
	"os"
)

func main() {
	s, e := release.Official()
	if e != nil {
		fail(e)
	}
	if os.Getenv("GITHUB_REPOSITORY") != s.Owner+"/"+s.Repository {
		fail(fmt.Errorf("configured official source does not match GITHUB_REPOSITORY"))
	}
	key, e := base64.StdEncoding.DecodeString(os.Getenv("PROCLY_SIGNING_KEY"))
	if e != nil || len(key) != ed25519.PrivateKeySize {
		fail(fmt.Errorf("release signing key is missing or invalid"))
	}
	public := ed25519.PrivateKey(key).Public().(ed25519.PublicKey)
	if base64.StdEncoding.EncodeToString(public) != s.PublicKey {
		fail(fmt.Errorf("private key does not match the embedded public key"))
	}
	fmt.Println("Official repository and signing identity verified.")
}
func fail(e error) { fmt.Fprintln(os.Stderr, e); os.Exit(1) }
