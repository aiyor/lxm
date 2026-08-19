package common

import "github.com/aiyor/lxm/internal/provider"

// TranslateBootModeToDaemon translates lxm VM boot.mode into daemon security.secureboot and security.csm.
// It is strictly gated on instType == InstanceTypeVM to protect container configs from mutation.
func TranslateBootModeToDaemon(instType provider.InstanceType, cfg map[string]string) map[string]string {
	if cfg == nil {
		return nil
	}
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	if instType != provider.InstanceTypeVM && instType != "virtual-machine" {
		return out
	}
	if mode, ok := out["boot.mode"]; ok {
		switch mode {
		case "uefi-nosecureboot":
			out["security.secureboot"] = "false"
			delete(out, "security.csm")
		case "uefi-secureboot":
			out["security.secureboot"] = "true"
			delete(out, "security.csm")
		case "bios":
			out["security.secureboot"] = "false"
			out["security.csm"] = "true"
		}
		delete(out, "boot.mode")
	}
	return out
}

// TranslateDaemonToBootMode reconstructs lxm boot.mode from daemon security.secureboot and security.csm.
// It is strictly gated on instType == InstanceTypeVM to prevent injecting boot.mode into container configs.
func TranslateDaemonToBootMode(instType provider.InstanceType, cfg map[string]string) map[string]string {
	if cfg == nil {
		return nil
	}
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	if instType != provider.InstanceTypeVM && instType != "virtual-machine" {
		return out
	}
	if _, ok := out["boot.mode"]; !ok {
		switch {
		case out["security.csm"] == "true":
			out["boot.mode"] = "bios"
		case out["security.secureboot"] == "false":
			out["boot.mode"] = "uefi-nosecureboot"
		default:
			out["boot.mode"] = "uefi-secureboot"
		}
	}
	return out
}
