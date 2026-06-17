package server

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

func resolveID(clientID *string) string {
	if clientID != nil && *clientID != "" {
		return *clientID
	}
	return uuid.New().String()
}

// allowedKcliHintKeys defines the kcli parameters that may be forwarded to
// kweb. Keys not in this set are dropped and logged at warn level.
//
// SECURITY NOTE: Some allowed keys carry elevated risk on untrusted networks:
//   - cloudinit: arbitrary cloud-init userdata (can run scripts on the VM)
//   - cmdline: kernel boot parameters
//   - kernel, initrd: custom kernel/initramfs images
//   - iso: arbitrary ISO mount
//   - keys: SSH public key injection (also available via spec.ssh_keys)
//   - sharedfolders: exposes host paths to the VM
//   - yamlinventory: arbitrary kcli YAML configuration
//
// These are permitted because this provider targets trusted homelab networks.
// For untrusted environments, restrict this list or add per-key validation.
//
// Blocked keys (not in this map): cmds, scripts, files, rhnregister,
// networkwait, enableroot, rootpassword, and any other kcli parameter
// that directly executes commands on the hypervisor.
var allowedKcliHintKeys = map[string]bool{
	"image": true, "network": true, "pool": true, "numcpus": true,
	"memory": true, "disks": true, "nets": true, "dns": true,
	"domain": true, "reservedns": true, "reservehost": true,
	"start": true, "keys": true, "iso": true, "cloudinit": true,
	"autostart": true, "flavor": true, "storemetadata": true,
	"sharedfolders": true, "kernel": true, "initrd": true,
	"cmdline": true, "placement": true, "yamlinventory": true,
	"cpuhotplug": true, "memoryhotplug": true, "numa": true,
	"pcidevices": true, "tpm": true, "rng": true, "zerotier_net": true,
	"virttype": true, "tags": true,
	"ctlplanes": true, "workers": true, "version": true,
	"sdn": true, "network_type": true, "api_ip": true,
}

// mergeKcliHints copies safe kcli-specific parameters from provider_hints.kcli
// into the kweb params map. Only keys in allowedKcliHintKeys are forwarded.
// Keys in excludeKeys (e.g. "profile", "cluster_type") are also skipped.
func mergeKcliHints(hints *ProviderHints, params map[string]interface{}, excludeKeys ...string) {
	if hints == nil {
		return
	}
	kcli, ok := (*hints)["kcli"]
	if !ok {
		return
	}
	m, ok := kcli.(map[string]interface{})
	if !ok {
		return
	}
	skip := make(map[string]bool, len(excludeKeys))
	for _, k := range excludeKeys {
		skip[k] = true
	}
	for k, v := range m {
		if skip[k] {
			continue
		}
		if !allowedKcliHintKeys[k] {
			slog.Warn("dropped disallowed provider_hints.kcli key", "key", k)
			continue
		}
		if _, exists := params[k]; !exists {
			params[k] = v
		}
	}
}

func parseMemorySize(size string) (int, error) {
	size = strings.TrimSpace(size)
	if strings.HasSuffix(size, "TB") {
		v, err := strconv.Atoi(strings.TrimSuffix(size, "TB"))
		return v * 1024 * 1024, err
	}
	if strings.HasSuffix(size, "GB") {
		v, err := strconv.Atoi(strings.TrimSuffix(size, "GB"))
		return v * 1024, err
	}
	if strings.HasSuffix(size, "MB") {
		v, err := strconv.Atoi(strings.TrimSuffix(size, "MB"))
		return v, err
	}
	return 0, fmt.Errorf("unrecognized memory unit in %q", size)
}
