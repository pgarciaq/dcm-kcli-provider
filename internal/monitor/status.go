package monitor

import (
	"strings"
	"time"
)

const provisioningThreshold = 10 * time.Minute

func MapVMStatus(kwebStatus string, createdAt time.Time) string {
	switch strings.ToLower(kwebStatus) {
	case "up":
		return "RUNNING"
	case "down":
		if time.Since(createdAt) < provisioningThreshold {
			return "PROVISIONING"
		}
		return "STOPPED"
	case "paused":
		return "PAUSED"
	case "error", "crashed", "nostate":
		return "ERROR"
	case "shuttingdown":
		return "STOPPING"
	default:
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
