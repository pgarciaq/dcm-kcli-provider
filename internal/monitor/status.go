package monitor

import (
	"strings"
	"time"
)

const provisioningThreshold = 10 * time.Minute

func MapVMStatus(kwebStatus string, createdAt time.Time) string {
	status := strings.ToLower(kwebStatus)
	switch {
	case status == "up":
		return "RUNNING"
	case status == "down":
		if time.Since(createdAt) < provisioningThreshold {
			return "PROVISIONING"
		}
		return "STOPPED"
	case status == "paused":
		return "PAUSED"
	case status == "error" || status == "crashed" || status == "nostate":
		return "ERROR"
	case status == "shuttingdown":
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
