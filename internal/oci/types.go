// Package oci holds the OCI image-spec and Docker distribution types
// ThothDock reads, plus digest handling and platform selection.
package oci

import "time"

// Media types (OCI image-spec v1.1 and Docker image manifest v2 schema 2).
const (
	MediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIConfig      = "application/vnd.oci.image.config.v1+json"
	MediaTypeOCILayer       = "application/vnd.oci.image.layer.v1.tar"
	MediaTypeOCILayerGzip   = "application/vnd.oci.image.layer.v1.tar+gzip"
	MediaTypeOCILayerZstd   = "application/vnd.oci.image.layer.v1.tar+zstd"
	MediaTypeOCINDLayer     = "application/vnd.oci.image.layer.nondistributable.v1.tar"
	MediaTypeOCINDLayerGzip = "application/vnd.oci.image.layer.nondistributable.v1.tar+gzip"

	MediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerConfig       = "application/vnd.docker.container.image.v1+json"
	MediaTypeDockerLayerGzip    = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	MediaTypeDockerForeignLayer = "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip"
)

// Platform identifies an image's target.
type Platform struct {
	Architecture string   `json:"architecture"`
	OS           string   `json:"os"`
	OSVersion    string   `json:"os.version,omitempty"`
	OSFeatures   []string `json:"os.features,omitempty"`
	Variant      string   `json:"variant,omitempty"`
}

func (p Platform) String() string {
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// Descriptor points at content by digest and size.
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      Digest            `json:"digest"`
	Size        int64             `json:"size"`
	URLs        []string          `json:"urls,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Platform    *Platform         `json:"platform,omitempty"`
}

// Index is an OCI image index or a Docker manifest list.
type Index struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType,omitempty"`
	Manifests     []Descriptor `json:"manifests"`
}

// Manifest is an OCI image manifest or a Docker v2 schema 2 manifest.
type Manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType,omitempty"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
}

// ImageConfig is the image configuration blob.
type ImageConfig struct {
	Created      *time.Time      `json:"created,omitempty"`
	Author       string          `json:"author,omitempty"`
	Architecture string          `json:"architecture"`
	OS           string          `json:"os"`
	Variant      string          `json:"variant,omitempty"`
	Config       ContainerConfig `json:"config"`
	RootFS       RootFS          `json:"rootfs"`
	History      []History       `json:"history,omitempty"`
}

// ContainerConfig is the execution defaults an image carries.
type ContainerConfig struct {
	User         string              `json:"User,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	Env          []string            `json:"Env,omitempty"`
	Entrypoint   []string            `json:"Entrypoint,omitempty"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Volumes      map[string]struct{} `json:"Volumes,omitempty"`
	WorkingDir   string              `json:"WorkingDir,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	StopSignal   string              `json:"StopSignal,omitempty"`
}

// RootFS lists the uncompressed layer digests (diff IDs).
type RootFS struct {
	Type    string   `json:"type"`
	DiffIDs []Digest `json:"diff_ids"`
}

// History describes how a layer was built.
type History struct {
	Created    *time.Time `json:"created,omitempty"`
	CreatedBy  string     `json:"created_by,omitempty"`
	Comment    string     `json:"comment,omitempty"`
	EmptyLayer bool       `json:"empty_layer,omitempty"`
}

// IsIndex reports whether a media type is a multi-platform index.
func IsIndex(mt string) bool { return mt == MediaTypeOCIIndex || mt == MediaTypeDockerManifestList }

// IsManifest reports whether a media type is a single-platform manifest.
func IsManifest(mt string) bool { return mt == MediaTypeOCIManifest || mt == MediaTypeDockerManifest }

// LayerCompression classifies a layer media type: "gzip", "none", or an
// error for formats ThothDock cannot apply.
func LayerCompression(mt string) (string, error) {
	switch mt {
	case MediaTypeOCILayerGzip, MediaTypeOCINDLayerGzip, MediaTypeDockerLayerGzip:
		return "gzip", nil
	case MediaTypeOCILayer, MediaTypeOCINDLayer:
		return "none", nil
	case MediaTypeOCILayerZstd:
		return "", errUnsupportedLayer("zstd-compressed layers are not supported yet")
	case MediaTypeDockerForeignLayer:
		return "", errUnsupportedLayer("foreign (Windows) layers are not supported")
	}
	return "", errUnsupportedLayer("unknown layer media type " + mt)
}

type unsupportedLayer string

func (e unsupportedLayer) Error() string { return string(e) }

func errUnsupportedLayer(s string) error { return unsupportedLayer(s) }
