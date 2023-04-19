package drivelist

import "fmt"

// This is somewhat odd in that it doesn't care about accuracy as much
// as it cares about matching the advertised sizes of drives.  If I
// buy an 8TB drive, then I want this to call it an 8TB drive, not a
// 7.26TB drive, or an 8.001TB drive or whatever.
//
// Setting the cutoff point for unit changes to 1999; a 1920 GB device
// will be reported as 1920 GB, while a 2000 GB device will be
// reported as 2 TB.  SSD sizes tend to run a bit weird, so this
// likely isn't quite right, but it's good enough for now.
//
// Might be better off looking for trailing non-zero digits and using
// that before jumping up one size.
//
// Also, we should probably round before doing the /1000 in the for
// loop.

func FormatDiskSize(byteSize uint64) string {
	var size, exponent uint64

	if byteSize==0 {
		return ""
	}
	
	// this is the only case where singular/plural matters, so hard-code it.
	if byteSize==1 {
		return "1 byte"
	}

	for size, exponent = byteSize, 0; size > 1999; size, exponent = size/1000, exponent+3 {
		// Nothing
	}

	unit := "bytes"
	switch exponent {
	case 0:
		unit = "bytes"
	case 3:
		unit = "kB"
	case 6:
		unit = "MB"
	case 9:
		unit = "GB"
	case 12:
		unit = "TB"
	case 15:
		unit = "PB"
	case 18:
		unit = "EB"
	default:
		unit = fmt.Sprintf("*10^%d bytes", exponent) // shouldn't happen
	}

	return fmt.Sprintf("%d %s", size, unit)
}

