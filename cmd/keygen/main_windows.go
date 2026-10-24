//go:build windows

// keygen creates an Ed25519 release signing identity outside the repository.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return fmt.Errorf("LOCALAPPDATA is not set")
	}
	dir := filepath.Join(root, "Procly", "keys")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	user, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return e
	}
	sd, e := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if e != nil {
		return e
	}
	acl, _, e := sd.DACL()
	if e != nil {
		return e
	}
	if e = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); e != nil {
		return e
	}
	path := filepath.Join(dir, "release-signing.key")
	public, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return fmt.Errorf("will not overwrite an existing signing key: %w", e)
	}
	if _, e = f.WriteString(base64.StdEncoding.EncodeToString(private) + "\n"); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	sourcePath := filepath.Join("internal", "release", "source.json")
	b, e := os.ReadFile(sourcePath)
	if e != nil {
		return e
	}
	var source map[string]string
	if e = json.Unmarshal(b, &source); e != nil {
		return e
	}
	source["signing_public_key"] = base64.StdEncoding.EncodeToString(public)
	b, e = json.MarshalIndent(source, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(sourcePath, append(b, '\n'), 0644); e != nil {
		return e
	}
	fmt.Println("Private key saved with access restricted to the current Windows user:", path)
	fmt.Println("Public key written to", sourcePath)
	return nil
}
