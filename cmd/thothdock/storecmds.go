package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Lord1Egypt/ThothDock/internal/engine"
	"github.com/Lord1Egypt/ThothDock/internal/image"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

func openImages(o *options) (*image.Store, *store.Blobs, error) {
	l, err := o.layout()
	if err != nil {
		return nil, nil, err
	}
	blobs := store.NewBlobs(l.Blobs(), l.Tmp())
	is, err := image.Open(l.Images(), filepath.Join(l.Root, "refs.json"), l.Tmp(), blobs, newLogger(false))
	return is, blobs, err
}

func inspectStore(args []string) error {
	fs := flag.NewFlagSet("inspect-store", flag.ContinueOnError)
	var o options
	o.register(fs)
	verify := fs.Bool("verify", false, "re-hash every blob")
	if err := fs.Parse(args); err != nil {
		return err
	}
	l, err := o.layout()
	if err != nil {
		return err
	}
	lock, err := lockRoot(l)
	if err != nil {
		return fmt.Errorf("%w (stop the daemon first: inspect-store opens the store directly)", err)
	}
	defer lock.Close()
	is, blobs, err := openImages(&o)
	if err != nil {
		return err
	}
	fmt.Printf("data root: %s\n\nIMAGES\n", l.Root)
	for _, img := range is.List() {
		fmt.Printf("  %s  %-10s %8.1f MB  tags=%v digests=%v layers=%d\n", img.ID.Hex()[:12], img.Platform, float64(img.Size)/1e6, img.RepoTags, img.RepoDigests, len(img.Layers))
	}
	used := is.ReferencedBlobs()
	all, err := blobs.List()
	if err != nil {
		return err
	}
	fmt.Printf("\nBLOBS (%d)\n", len(all))
	bad := 0
	for _, d := range all {
		state := "referenced"
		if !used[d] {
			state = "UNREFERENCED (gc removes it)"
		}
		if *verify {
			if err := blobs.Verify(d); err != nil {
				state += " CORRUPT: " + err.Error()
				bad++
			} else {
				state += " verified"
			}
		}
		fi, _ := os.Stat(blobs.Path(d))
		fmt.Printf("  %s %10d  %s\n", d, fi.Size(), state)
	}
	fmt.Println("\nCONTAINERS")
	var recs []engine.Record
	entries, _ := os.ReadDir(l.Containers())
	for _, e := range entries {
		var r engine.Record
		if store.ReadJSON(filepath.Join(l.Containers(), e.Name(), "config.json"), &r) == nil {
			recs = append(recs, r)
		}
	}
	for _, r := range recs {
		fmt.Printf("  %s  %-24s %-8s exit=%d image=%s\n", r.ID[:12], r.Name, r.State.Status, r.State.ExitCode, r.Image.Hex()[:12])
	}
	if bad > 0 {
		return fmt.Errorf("%d corrupt blobs", bad)
	}
	return nil
}

func gc(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	var o options
	o.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	l, err := o.layout()
	if err != nil {
		return err
	}
	lock, err := lockRoot(l)
	if err != nil {
		return fmt.Errorf("%w (stop the daemon before gc)", err)
	}
	defer lock.Close()
	if err := clearTmp(l); err != nil {
		return err
	}
	is, blobs, err := openImages(&o) // also removes interrupted pulls
	if err != nil {
		return err
	}
	used := is.ReferencedBlobs()
	all, err := blobs.List()
	if err != nil {
		return err
	}
	var freed int64
	removed := 0
	for _, d := range all {
		if used[d] {
			continue
		}
		if fi, err := os.Stat(blobs.Path(d)); err == nil {
			freed += fi.Size()
		}
		if err := blobs.Delete(d); err != nil {
			return err
		}
		removed++
	}
	fmt.Printf("removed %d unreferenced blobs, %.1f MB\n", removed, float64(freed)/1e6)
	return nil
}
