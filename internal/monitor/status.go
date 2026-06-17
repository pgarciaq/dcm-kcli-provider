package monitor

import (
	"log/slog"
	"strings"
	"time"
)

const provisioningThreshold = 10 * time.Minute

func eqf(s, target string) bool { return strings.EqualFold(s, target) }

// MapVMStatus maps kweb VM status strings to DCM canonical states.
// Uses EqualFold for zero-allocation case-insensitive comparison.
func MapVMStatus(kwebStatus string, createdAt time.Time) string {
	if eqf(kwebStatus, "up") || eqf(kwebStatus, "running") {
		return "RUNNING"
	}
	if eqf(kwebStatus, "down") || eqf(kwebStatus, "shutoff") {
		if time.Since(createdAt) < provisioningThreshold {
			return "PROVISIONING"
		}
		return "STOPPED"
	}
	if eqf(kwebStatus, "paused") || eqf(kwebStatus, "suspended") {
		return "PAUSED"
	}
	if eqf(kwebStatus, "error") || eqf(kwebStatus, "crashed") || eqf(kwebStatus, "nostate") || eqf(kwebStatus, "fault") {
		return "ERROR"
	}
	if eqf(kwebStatus, "shuttingdown") || eqf(kwebStatus, "stopping") || eqf(kwebStatus, "powering-off") {
		return "STOPPING"
	}
	slog.Warn("unrecognized kweb VM status, mapping to ERROR", "kweb_status", kwebStatus)
	return "ERROR"
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
