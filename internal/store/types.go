package store

import "time"

// HostIdentity is how an agent names its host.
type HostIdentity struct {
	MachineID    string
	Hostname     string
	OS           string
	AgentVersion string
	BootID       string    // the kernel's boot id; "" from agents before 0.9
	BootedAt     time.Time // zero when unknown
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
	Expander      string // the kernel's name of the expander in the path, "" for the HBA's own ports
	ExpanderID    string // the expander's SAS address; "" from agents before 0.4
	Bay           string
	EnclosurePath string
	// Where the drive physically is; see enclosureKey. Empty from agents
	// before 0.7, which knew only expanders.
	EnclosureID    string // the SES enclosure identifier
	EnclosureVia   string // the node whose port reaches it: "expander-11:0", "host11"
	EnclosureViaID string // that node's SAS address
	EnclosureModel string // what the enclosure is, when the agent knows; "" leaves it to the SAS topology
	EnclosureBoard string // the DMI board name with it
	Uses           []string
	DevLinks       []string
	Error          string // non-empty: identity unreadable
	MemberState    string
	SCSIAddr       string
}

// ReportBay is an enclosure bay with nothing in it, as an agent reports it.
type ReportBay struct {
	EnclosureID    string
	EnclosureVia   string
	EnclosureViaID string
	EnclosureModel string
	EnclosureBoard string
	Bay            string
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
	EmptyBays       []ReportBay
	SASNodes        []SASNode // empty from agents before 0.6 or hosts without SAS
	SASPhys         []SASPhy
	DIMMs           []DIMM // empty from agents before 0.9 or hosts that describe none
	MemTotalBytes   uint64 // the kernel's MemTotal; 0 when the agent did not say
}

// DIMM is one memory module as an agent reports it: the firmware's
// description joined to EDAC's counts. See collect.DIMM.
type DIMM struct {
	Slot, Bank   string
	SizeBytes    uint64
	Ranks        int
	Type         string
	SpeedMTs     int
	Manufacturer string
	Part, Serial string
	EDAC         string // "mc0/csrow2/ch2+mc0/csrow3/ch2"
	EDACType     string
	EDACBytes    uint64 // what the matched EDAC entries add up to
	Mapping      string // "exact" | "inferred" | ""
	CE, UE       uint64 // since boot
}

// Key is what a module is tracked by: its slot, else its EDAC location.
func (d DIMM) Key() string {
	if d.Slot != "" {
		return d.Slot
	}
	return d.EDAC
}

// SASNode is an HBA or expander as an agent reports it.
type SASNode struct {
	Kind          string // "hba" | "expander"
	Name          string
	Address       string // the key
	Vendor        string
	Product       string
	Revision      string
	ParentAddress string
	UpstreamPort  string
}

// SASPhy is one phy of a node as an agent reports it: link, far end, and
// the four error counters, cumulative since boot.
type SASPhy struct {
	OwnerAddress    string
	PhyID           int
	Name            string
	Port            string
	PortWidth       int
	Rate            string
	RateGbit        float64
	AttachedKind    string // "expander" | "drive" | "device" | "upstream" | ""
	Attached        string
	AttachedAddress string
	DevName         string
	Bay             string
	Enabled         bool
	InvalidDword    uint64
	DisparityError  uint64
	LossDwordSync   uint64
	PhyResetProblem uint64
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
	EventEnclosureRenamed   = "enclosure_renamed" // host-level: an enclosure's key changed under every drive in it (before 0.7: expander_renamed)
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
	EventSASLinkChanged     = "sas_link_changed"     // a phy's negotiated rate changed, or its link came or went
	EventSASAttachedChanged = "sas_attached_changed" // something else is on the far end of a phy
	EventSASPortChanged     = "sas_port_changed"     // a port bundles a different number of phys
	EventSASErrors          = "sas_errors"           // a phy's error counters grew; once per phy per day
	EventSASNodeChanged     = "sas_node_changed"     // an HBA or expander appeared, vanished, or changed firmware
	EventHostMerged         = "host_merged"
	EventHostRebooted       = "host_rebooted"  // host-level: the agent reports a new boot id
	EventHardwareError      = "hardware_error" // host-level: the kernel logged a memory or machine-check error; once per class, location and day
	EventMemoryErrors       = "memory_errors"  // host-level: a module's EDAC counts grew; once per module per day
	EventDimmChanged        = "dimm_changed"   // host-level: a module appeared, vanished, or was replaced (its serial changed)
)

// Placement end reasons.
const (
	EndVanished   = "vanished"
	EndMoved      = "moved"
	EndUseChanged = "use_changed"
	EndMerged     = "merged"
)
