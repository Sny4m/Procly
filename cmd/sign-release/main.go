// sign-release authenticates GoReleaser's checksum manifest with an offline
// provisioned Ed25519 key. No private key is generated or committed by CI.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: sign-release checksums.txt")
		os.Exit(1)
	}
	key, e := base64.StdEncoding.DecodeString(os.Getenv("PROCLY_SIGNING_KEY"))
	if e != nil || len(key) != ed25519.PrivateKeySize {
		fmt.Fprintln(os.Stderr, "PROCLY_SIGNING_KEY must be a base64 Ed25519 private key")
		os.Exit(1)
	}
	b, e := os.ReadFile(os.Args[1])
	if e != nil {
		panic(e)
	}
	sig := ed25519.Sign(key, b)
	if e = os.WriteFile(os.Args[1]+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0600); e != nil {
		panic(e)
	}
}
