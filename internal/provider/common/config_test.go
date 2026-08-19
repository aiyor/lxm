package common_test

import (
	"reflect"
	"testing"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/common"
)

func TestTranslateBootModeToDaemon(t *testing.T) {
	// 1. Container safety: container configs must remain completely untouched
	containerCfg := map[string]string{"boot.mode": "uefi-nosecureboot", "user.lxm": "true"}
	outContainer := common.TranslateBootModeToDaemon(provider.InstanceTypeContainer, containerCfg)
	if !reflect.DeepEqual(outContainer, containerCfg) {
		t.Errorf("container config was modified: got %+v, want %+v", outContainer, containerCfg)
	}

	// 2. VM: uefi-nosecureboot
	vmCfg1 := map[string]string{"boot.mode": "uefi-nosecureboot", "image.os": "ubuntu"}
	outVM1 := common.TranslateBootModeToDaemon(provider.InstanceTypeVM, vmCfg1)
	expected1 := map[string]string{"security.secureboot": "false", "image.os": "ubuntu"}
	if !reflect.DeepEqual(outVM1, expected1) {
		t.Errorf("VM uefi-nosecureboot got %+v, want %+v", outVM1, expected1)
	}

	// 3. VM: uefi-secureboot
	vmCfg2 := map[string]string{"boot.mode": "uefi-secureboot"}
	outVM2 := common.TranslateBootModeToDaemon(provider.InstanceTypeVM, vmCfg2)
	expected2 := map[string]string{"security.secureboot": "true"}
	if !reflect.DeepEqual(outVM2, expected2) {
		t.Errorf("VM uefi-secureboot got %+v, want %+v", outVM2, expected2)
	}

	// 4. VM: bios
	vmCfg3 := map[string]string{"boot.mode": "bios"}
	outVM3 := common.TranslateBootModeToDaemon(provider.InstanceTypeVM, vmCfg3)
	expected3 := map[string]string{"security.secureboot": "false", "security.csm": "true"}
	if !reflect.DeepEqual(outVM3, expected3) {
		t.Errorf("VM bios got %+v, want %+v", outVM3, expected3)
	}
}

func TestTranslateDaemonToBootMode(t *testing.T) {
	// 1. Container safety: container configs must remain untouched
	containerCfg := map[string]string{"security.secureboot": "false"}
	outContainer := common.TranslateDaemonToBootMode(provider.InstanceTypeContainer, containerCfg)
	if _, hasBootMode := outContainer["boot.mode"]; hasBootMode {
		t.Errorf("boot.mode was injected into container config: %+v", outContainer)
	}

	// 2. VM: bios
	vmCfg1 := map[string]string{"security.secureboot": "false", "security.csm": "true"}
	outVM1 := common.TranslateDaemonToBootMode(provider.InstanceTypeVM, vmCfg1)
	if outVM1["boot.mode"] != "bios" {
		t.Errorf("expected boot.mode=bios, got %q", outVM1["boot.mode"])
	}

	// 3. VM: uefi-nosecureboot
	vmCfg2 := map[string]string{"security.secureboot": "false", "security.csm": "false"}
	outVM2 := common.TranslateDaemonToBootMode(provider.InstanceTypeVM, vmCfg2)
	if outVM2["boot.mode"] != "uefi-nosecureboot" {
		t.Errorf("expected boot.mode=uefi-nosecureboot, got %q", outVM2["boot.mode"])
	}

	// 4. VM: uefi-secureboot (explicit)
	vmCfg3 := map[string]string{"security.secureboot": "true"}
	outVM3 := common.TranslateDaemonToBootMode(provider.InstanceTypeVM, vmCfg3)
	if outVM3["boot.mode"] != "uefi-secureboot" {
		t.Errorf("expected boot.mode=uefi-secureboot, got %q", outVM3["boot.mode"])
	}

	// 5. VM: uefi-secureboot (omitted on VM defaults to uefi-secureboot)
	vmCfg4 := map[string]string{"image.os": "ubuntu"}
	outVM4 := common.TranslateDaemonToBootMode(provider.InstanceTypeVM, vmCfg4)
	if outVM4["boot.mode"] != "uefi-secureboot" {
		t.Errorf("expected omitted secureboot on VM to map to boot.mode=uefi-secureboot, got %q", outVM4["boot.mode"])
	}

	// 6. Container: omitted secureboot should NOT set boot.mode
	containerCfg2 := map[string]string{"image.os": "ubuntu"}
	outContainer2 := common.TranslateDaemonToBootMode(provider.InstanceTypeContainer, containerCfg2)
	if _, hasBootMode := outContainer2["boot.mode"]; hasBootMode {
		t.Errorf("expected container with omitted secureboot to not have boot.mode, got %+v", outContainer2)
	}
}
