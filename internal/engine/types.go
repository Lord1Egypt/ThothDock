package engine

import (
	"encoding/json"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/oci"
)

// StrSlice decodes a JSON string or array of strings (Docker's strslice).
type StrSlice []string

func (s *StrSlice) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = nil
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*s = StrSlice{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	if many == nil {
		many = []string{}
	}
	*s = many
	return nil
}

// ContainerConfig is the portable part of POST /containers/create
// (Docker Engine API v1.41 ContainerConfig).
type ContainerConfig struct {
	Hostname        string              `json:"Hostname"`
	Domainname      string              `json:"Domainname"`
	User            string              `json:"User"`
	AttachStdin     bool                `json:"AttachStdin"`
	AttachStdout    bool                `json:"AttachStdout"`
	AttachStderr    bool                `json:"AttachStderr"`
	ExposedPorts    map[string]struct{} `json:"ExposedPorts,omitempty"`
	Tty             bool                `json:"Tty"`
	OpenStdin       bool                `json:"OpenStdin"`
	StdinOnce       bool                `json:"StdinOnce"`
	Env             []string            `json:"Env"`
	Cmd             StrSlice            `json:"Cmd"`
	Healthcheck     json.RawMessage     `json:"Healthcheck,omitempty"`
	ArgsEscaped     bool                `json:"ArgsEscaped,omitempty"`
	Image           string              `json:"Image"`
	Volumes         map[string]struct{} `json:"Volumes"`
	WorkingDir      string              `json:"WorkingDir"`
	Entrypoint      StrSlice            `json:"Entrypoint"`
	NetworkDisabled bool                `json:"NetworkDisabled,omitempty"`
	MacAddress      string              `json:"MacAddress,omitempty"`
	OnBuild         []string            `json:"OnBuild"`
	Labels          map[string]string   `json:"Labels"`
	StopSignal      string              `json:"StopSignal,omitempty"`
	StopTimeout     *int                `json:"StopTimeout,omitempty"`
	Shell           StrSlice            `json:"Shell,omitempty"`
}

// PortBinding is a host side of a published port.
type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// RestartPolicy is HostConfig.RestartPolicy.
type RestartPolicy struct {
	Name              string `json:"Name"`
	MaximumRetryCount int    `json:"MaximumRetryCount"`
}

// LogConfig is HostConfig.LogConfig.
type LogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

// HostConfig is the host-dependent part of POST /containers/create. Only
// the fields ThothDock reads or must refuse are decoded; see
// docs/COMPATIBILITY.md.
type HostConfig struct {
	Binds           []string                 `json:"Binds"`
	NetworkMode     string                   `json:"NetworkMode"`
	PortBindings    map[string][]PortBinding `json:"PortBindings"`
	RestartPolicy   RestartPolicy            `json:"RestartPolicy"`
	AutoRemove      bool                     `json:"AutoRemove"`
	VolumesFrom     []string                 `json:"VolumesFrom"`
	Mounts          []json.RawMessage        `json:"Mounts,omitempty"`
	CapAdd          []string                 `json:"CapAdd"`
	CapDrop         []string                 `json:"CapDrop"`
	Dns             []string                 `json:"Dns"`
	DnsOptions      []string                 `json:"DnsOptions"`
	DnsSearch       []string                 `json:"DnsSearch"`
	ExtraHosts      []string                 `json:"ExtraHosts"`
	GroupAdd        []string                 `json:"GroupAdd"`
	IpcMode         string                   `json:"IpcMode"`
	Links           []string                 `json:"Links"`
	PidMode         string                   `json:"PidMode"`
	Privileged      bool                     `json:"Privileged"`
	PublishAllPorts bool                     `json:"PublishAllPorts"`
	ReadonlyRootfs  bool                     `json:"ReadonlyRootfs"`
	SecurityOpt     []string                 `json:"SecurityOpt"`
	Tmpfs           map[string]string        `json:"Tmpfs,omitempty"`
	UTSMode         string                   `json:"UTSMode"`
	UsernsMode      string                   `json:"UsernsMode"`
	Sysctls         map[string]string        `json:"Sysctls,omitempty"`
	Runtime         string                   `json:"Runtime,omitempty"`
	LogConfig       LogConfig                `json:"LogConfig"`
	ConsoleSize     [2]uint                  `json:"ConsoleSize"`
	Init            *bool                    `json:"Init,omitempty"`
	CgroupnsMode    string                   `json:"CgroupnsMode,omitempty"`

	// Resource limits: accepted only at their "unset" values.
	CpuShares          int64             `json:"CpuShares"`
	Memory             int64             `json:"Memory"`
	CpuPeriod          int64             `json:"CpuPeriod"`
	CpuQuota           int64             `json:"CpuQuota"`
	CpusetCpus         string            `json:"CpusetCpus"`
	CpusetMems         string            `json:"CpusetMems"`
	NanoCpus           int64             `json:"NanoCpus"`
	MemoryReservation  int64             `json:"MemoryReservation"`
	MemorySwap         int64             `json:"MemorySwap"`
	PidsLimit          *int64            `json:"PidsLimit"`
	BlkioWeight        uint16            `json:"BlkioWeight"`
	OomKillDisable     *bool             `json:"OomKillDisable"`
	Devices            []json.RawMessage `json:"Devices"`
	DeviceRequests     []json.RawMessage `json:"DeviceRequests,omitempty"`
	Ulimits            []json.RawMessage `json:"Ulimits"`
	CgroupParent       string            `json:"CgroupParent"`
	ShmSize            int64             `json:"ShmSize"`
	BlkioDeviceReadBps []json.RawMessage `json:"BlkioDeviceReadBps,omitempty"`
}

// CreateRequest is the POST /containers/create body.
type CreateRequest struct {
	ContainerConfig
	HostConfig       *HostConfig     `json:"HostConfig"`
	NetworkingConfig json.RawMessage `json:"NetworkingConfig,omitempty"`
}

// Container states. ThothDock keeps them explicit; the API maps
// starting->created and failed->exited (with State.Error).
const (
	StatusCreated  = "created"
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusExited   = "exited"
	StatusFailed   = "failed"
	StatusRemoving = "removing"
)

// State is a container's persisted run state.
type State struct {
	Status     string    `json:"status"`
	Pid        int       `json:"pid"`
	PidStart   uint64    `json:"pidStart"` // /proc/<pid>/stat starttime, guards PID reuse
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	ExitCode   int       `json:"exitCode"`
	Error      string    `json:"error"`
}

// Record is what is persisted for a container (containers/<id>/config.json).
type Record struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Created      time.Time       `json:"created"`
	Image        oci.Digest      `json:"image"`
	ImageRef     string          `json:"imageRef"`
	Config       ContainerConfig `json:"config"`
	HostConfig   HostConfig      `json:"hostConfig"`
	Path         string          `json:"path"`
	Args         []string        `json:"args"`
	Binds        []BindRecord    `json:"binds"`
	State        State           `json:"state"`
	RestartCount int             `json:"restartCount"`
}

// BindRecord is an approved bind mount.
type BindRecord struct {
	Source string `json:"source"`
	Target string `json:"target"`
	// Volume is set for a named volume; Source is then its data directory
	// when the container was created, and is recomputed at every start.
	Volume string `json:"volume,omitempty"`
}
