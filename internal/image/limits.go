package image

import (
	"errors"
	"math"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/layer"
)

// Limits are the safety ceilings of a pull. They exist so that an image a
// user pulls deliberately, or a registry that lies about sizes, cannot fill
// the device. A zero field means the default; a negative field disables
// that one limit. The defaults are far above ordinary images (the largest
// common ones, such as CUDA and PyTorch bases, are single-digit GiB) and
// are accounted on bytes actually read and written, never on what a
// manifest claims.
type Limits struct {
	// MaxLayers is the number of layers an image may have.
	MaxLayers int
	// MaxCompressedBytes bounds the sum of the layer sizes the manifest
	// declares, checked before anything is downloaded. Each blob is also
	// held to its declared size while it downloads.
	MaxCompressedBytes int64
	// MaxLayerBytes bounds one layer's uncompressed tar stream.
	MaxLayerBytes int64
	// MaxExtractedBytes bounds the bytes written for the whole image: file
	// contents, plus hard links the platform forces into copies. Bytes
	// written, not net size: a file a later layer deletes still counts.
	MaxExtractedBytes int64
	// MaxEntries bounds the tar entries of the whole image.
	MaxEntries int64
	// MinFreeBytes is how much free space must remain on the filesystem
	// holding the image store. It is checked before each download and
	// about every 32 MiB during extraction, where the platform reports free
	// space (statfs); where it does not, only the ceilings above apply.
	MinFreeBytes int64
}

// DefaultLimits returns the defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxLayers:          128,
		MaxCompressedBytes: 8 << 30,
		MaxLayerBytes:      16 << 30,
		MaxExtractedBytes:  16 << 30,
		MaxEntries:         4_000_000,
		MinFreeBytes:       512 << 20,
	}
}

// resolved replaces zero fields with the defaults and negative ones with
// "unlimited" (represented as 0 where the consumers treat 0 as off).
func (l Limits) resolved() Limits {
	d := DefaultLimits()
	if l.MaxLayers == 0 {
		l.MaxLayers = d.MaxLayers
	}
	pick := func(v *int64, def int64) {
		if *v == 0 {
			*v = def
		}
	}
	pick(&l.MaxCompressedBytes, d.MaxCompressedBytes)
	pick(&l.MaxLayerBytes, d.MaxLayerBytes)
	pick(&l.MaxExtractedBytes, d.MaxExtractedBytes)
	pick(&l.MaxEntries, d.MaxEntries)
	pick(&l.MinFreeBytes, d.MinFreeBytes)
	return l
}

// freeBytes reports the space available to this app on the filesystem of
// path; ok is false where the platform does not say. A variable so tests
// can simulate a full disk.
var freeBytes = func(path string) (free uint64, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}

// ErrNoSpace is returned when the free-space floor would be crossed.
var ErrNoSpace = errors.New("not enough free storage")

// ensureSpace fails when need bytes plus the free-space floor do not fit.
func (l Limits) ensureSpace(dir string, need int64) error {
	if l.MinFreeBytes < 0 {
		return nil
	}
	free, ok := freeBytes(dir)
	if !ok {
		return nil
	}
	if need < 0 {
		need = 0
	}
	if free < uint64(l.MinFreeBytes) || free-uint64(l.MinFreeBytes) < uint64(need) {
		return errdefs.Invalid("%w: %d bytes available, %d needed and %d must stay free (--min-free-bytes)", ErrNoSpace, free, need, l.MinFreeBytes)
	}
	return nil
}

// checkManifest applies the limits that need only the manifest.
func (l Limits) checkManifest(sizes []int64) error {
	if l.MaxLayers > 0 && len(sizes) > l.MaxLayers {
		return errdefs.Invalid("image has %d layers; the limit is %d (--max-layers)", len(sizes), l.MaxLayers)
	}
	var total int64
	for _, s := range sizes {
		if s < 0 || total > math.MaxInt64-s {
			return errdefs.Invalid("manifest declares an invalid layer size")
		}
		total += s
	}
	if l.MaxCompressedBytes > 0 && total > l.MaxCompressedBytes {
		return errdefs.Invalid("image layers total %d bytes compressed; the limit is %d (--max-compressed-bytes)", total, l.MaxCompressedBytes)
	}
	return nil
}

// layerLimits are the limits for one layer, given what the earlier layers
// of the same image already used.
func (l Limits) layerLimits(dir string, usedBytes, usedEntries int64) layer.Limits {
	ll := layer.Limits{MaxStreamBytes: max(l.MaxLayerBytes, 0)}
	if l.MaxExtractedBytes > 0 {
		ll.MaxFileBytes = max(l.MaxExtractedBytes-usedBytes, 1)
	}
	if l.MaxEntries > 0 {
		ll.MaxEntries = max(l.MaxEntries-usedEntries, 1)
	}
	if l.MinFreeBytes >= 0 {
		ll.CheckSpace = func() error { return l.ensureSpace(dir, 0) }
	}
	return ll
}

// limitMessage explains a layer.LimitError with the option that raises it.
func limitMessage(err error) error {
	var le *layer.LimitError
	if !errors.As(err, &le) {
		return err
	}
	opt := map[string]string{
		"uncompressed size": "--max-layer-bytes or --max-extracted-bytes",
		"extracted size":    "--max-extracted-bytes",
		"entry count":       "--max-entries",
	}[le.What]
	return errdefs.Invalid("image extraction stopped, the partial result was discarded: %v (%s)", err, opt)
}
