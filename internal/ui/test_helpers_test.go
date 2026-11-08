package ui

import (
	"io/fs"
	"testing"
)

func mustReadStatic(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(assets, "static/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
