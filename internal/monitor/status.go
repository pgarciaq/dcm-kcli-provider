package monitor

import (
	"log/slog"
	"strings"
	"time"
)

const provisioningThreshold = 10 * time.Minute

// MapVMStatus maps kweb VM status strings to DCM canonical states.
// kweb typically returns lowercase values but we normalize defensively.
func MapVMStatus(kwebStatus string, createdAt time.Time) string {
	switch strings.ToLower(kwebStatus) {
	case "up", "running":
		return "RUNNING"
	case "down", "shutoff":
		if time.Since(createdAt) < provisioningThreshold {
			return "PROVISIONING"
		}
		return "STOPPED"
	case "paused", "suspended":
		return "PAUSED"
	case "error", "crashed", "nostate", "fault":
		return "ERROR"
	case "shuttingdown", "stopping", "powering-off":
		return "STOPPING"
	default:
		slog.Warn("unrecognized kweb VM status, mapping to ERROR", "kweb_status", kwebStatus)
		return "ERROR"
	}
}

func MapClusterStatus(hasNodes bool, createdAt time.Time, clusterCreateTimeout time.Duration) string {
	if hasNodes {
		return "ACTIVE"
	}
	if time.Since(createdAt) > clusterCreateTimeout {
		return "ERROR"
	}
	return "CREATING"
}
