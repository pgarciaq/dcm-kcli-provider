package server

import (
	"testing"
)

func TestMergeKcliHints_DropsUnknownKeys(t *testing.T) {
	hints := ProviderHints{
		"kcli": map[string]interface{}{
			"cmds":    "rm -rf /",
			"scripts": "malicious.sh",
			"image":   "fedora41",
		},
	}
	params := map[string]interface{}{}
	mergeKcliHints(&hints, params)

	if _, ok := params["cmds"]; ok {
		t.Error("expected disallowed key cmds to be dropped")
	}
	if _, ok := params["scripts"]; ok {
		t.Error("expected disallowed key scripts to be dropped")
	}
	if params["image"] != "fedora41" {
		t.Errorf("expected allowed key image to be forwarded, got %v", params["image"])
	}
}

func TestMergeKcliHints_ForwardsAllowedKeys(t *testing.T) {
	hints := ProviderHints{
		"kcli": map[string]interface{}{
			"network": "default",
			"memory":  "4096",
			"numcpus": 2,
		},
	}
	params := map[string]interface{}{}
	mergeKcliHints(&hints, params)

	if params["network"] != "default" {
		t.Errorf("expected network=default, got %v", params["network"])
	}
	if params["memory"] != "4096" {
		t.Errorf("expected memory=4096, got %v", params["memory"])
	}
	if params["numcpus"] != 2 {
		t.Errorf("expected numcpus=2, got %v", params["numcpus"])
	}
}

func TestMergeKcliHints_SkipsExcludedKeys(t *testing.T) {
	hints := ProviderHints{
		"kcli": map[string]interface{}{
			"profile":      "fedora-39",
			"cluster_type": "k3s",
			"image":        "fedora41",
		},
	}
	params := map[string]interface{}{}
	mergeKcliHints(&hints, params, "profile", "cluster_type")

	if _, ok := params["profile"]; ok {
		t.Error("expected excluded key profile to be skipped")
	}
	if _, ok := params["cluster_type"]; ok {
		t.Error("expected excluded key cluster_type to be skipped")
	}
	if params["image"] != "fedora41" {
		t.Errorf("expected image=fedora41, got %v", params["image"])
	}
}

func TestMergeKcliHints_DoesNotOverwriteExistingParams(t *testing.T) {
	hints := ProviderHints{
		"kcli": map[string]interface{}{
			"network": "hint-net",
		},
	}
	params := map[string]interface{}{
		"network": "existing-net",
	}
	mergeKcliHints(&hints, params)

	if params["network"] != "existing-net" {
		t.Errorf("expected existing param to be preserved, got %v", params["network"])
	}
}

func TestMergeKcliHints_NilHints(t *testing.T) {
	params := map[string]interface{}{"existing": "value"}
	mergeKcliHints(nil, params)
	if len(params) != 1 {
		t.Errorf("expected params unchanged, got %v", params)
	}
}
