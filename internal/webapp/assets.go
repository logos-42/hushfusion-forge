package webapp

import (
	"embed"
	"io/fs"
)

//go:embed assets/*
var assetsRaw embed.FS

var assets = func() fs.FS {
	sub, err := fs.Sub(assetsRaw, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}()
