package store

import "time"

// HostIdentity is how an agent names its host.
type HostIdentity struct {
	MachineID    string
	Hostname     string
	OS           string
	AgentVersion string
}

// DriveIdentity is how an agent names a drive. See Keys for how it is
// matched.
type DriveIdentity struct {
	WWN    string
	Vendor string
	Model  string
	Serial string
}

// Keys lists the identity keys for a drive, strongest first: the WWN when
// there is one, then model plus serial. A drive with neither has no keys and
// is never matched.
func (id DriveIdentity) Keys() []string {
	var keys []string
	if id.WWN != "" {
		keys = append(keys, "wwn:"+id.WWN)
	}
	if id.Serial != "" {
		keys = append(keys, "serial:"+id.Model+"/"+id.Serial)
	}
	return keys
}

// ReportDevice is one device as an agent reports it.
type ReportDevice struct {
	DevName       string
	Identity      DriveIdentity
	Bus           string
	SizeBytes     uint64
	Expander      string // the kernel's name
	ExpanderID    string // the SAS address; "" from agents before 0.4
	Bay           string
	EnclosurePath string
	Uses          []string
	DevLinks      []string
	Error         string // non-empty: identity unreadable
	MemberState   string
	SCSIAddr      string
}

// PoolMember is a pool member the agent could not map to a device.
type PoolMember struct {
	Pool  string
	Path  string
	GUID  string
	State string
}

// Report is one host's complete inventory at one moment.
type Report struct {
	Host            HostIdentity
	ObservedAt      time.Time
	Devices         []ReportDevice
	Unmapped        []PoolMember
	Complete        bool // false if a collector stage failed outright
	CollectorErrors []string
}

// DriveStatus is the server's status for one drive, returned to the agent.
type DriveStatus struct {
	Identity DriveIdentity
	Status   string
	Note     string
}

// IngestResult says what Ingest did with a report.
type IngestResult struct {
	Accepted     bool
	RejectReason string // "clock_skew" | "stale"
	Changed      bool   // a new snapshot was recorded
	Statuses     []DriveStatus
}

// Drive statuses. The last three mean absence is expected.
const (
	StatusOK      = "ok"
	StatusSuspect = "suspect"
	StatusBad     = "bad"
	StatusShelved = "shelved"
	StatusRetired = "retired"
)

// ValidStatus reports whether s is one of the drive statuses.
func ValidStatus(s string) bool {
	switch s {
	case StatusOK, StatusSuspect, StatusBad, StatusShelved, StatusRetired:
		return true
	}
	return false
}

// AbsenceExpected reports whether a drive with this status is allowed to be
// missing without being listed as such.
func AbsenceExpected(status string) bool {
	return status == StatusBad || status == StatusShelved || status == StatusRetired
}

// Event kinds.
const (
	EventFirstSeen          = "first_seen"
	EventAppeared           = "appeared"
	EventVanished           = "vanished"
	EventReappeared         = "reappeared"
	EventMovedHost          = "moved_host"
	EventMovedBay           = "moved_bay"
	EventExpanderRenamed    = "expander_renamed" // host-level: an expander's key changed under every drive on it
	EventUseChanged         = "use_changed"
	EventMemberStateChanged = "member_state_changed"
	EventIdentityConflict   = "identity_conflict"
	EventStatusChanged      = "status_changed"
	EventNote               = "note"
	EventHostFirstSeen      = "host_first_seen"
	EventHostStale          = "host_stale"
	EventHostResumed        = "host_resumed"
	EventReportDegraded     = "report_degraded"
	EventPoolMissingMember  = "pool_missing_member"
	EventKernelWarning      = "kernel_warning"
	EventSmartWarning       = "smart_warning"
	EventMerged             = "merged"
	EventHostMerged         = "host_merged"
)

// Placement end reasons.
const (
	EndVanished   = "vanished"
	EndMoved      = "moved"
	EndUseChanged = "use_changed"
	EndMerged     = "merged"
)
