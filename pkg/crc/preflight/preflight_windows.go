package preflight

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/code-ready/crc/pkg/crc/network"
	"github.com/code-ready/crc/pkg/os/windows/powershell"
	"github.com/code-ready/crc/pkg/os/windows/win32"
)

var hypervPreflightChecks = []Check{
	{
		configKeySuffix:  "check-administrator-user",
		checkDescription: "Checking if running in a shell with administrator rights",
		check:            checkIfRunningAsNormalUser,
		fixDescription:   "crc should be ran in a shell without administrator rights",
		flags:            NoFix | StartUpOnly,

		labels: labels{Os: Windows},
	},
	{
		configKeySuffix:  "check-windows-version",
		checkDescription: "Checking Windows 10 release",
		check:            checkVersionOfWindowsUpdate,
		fixDescription:   "Please manually update your Windows 10 installation",
		flags:            NoFix | StartUpOnly,

		labels: labels{Os: Windows},
	},
	{
		configKeySuffix:  "check-windows-edition",
		checkDescription: "Checking Windows edition",
		check:            checkWindowsEdition,
		fixDescription:   "Your Windows edition is not supported. Consider using Professional or Enterprise editions of Windows",
		flags:            NoFix | StartUpOnly,

		labels: labels{Os: Windows},
	},
	{
		configKeySuffix:  "check-hyperv-installed",
		checkDescription: "Checking if Hyper-V is installed and operational",
		check:            checkHyperVInstalled,
		fixDescription:   "Installing Hyper-V",
		fix:              fixHyperVInstalled,
		flags:            StartUpOnly,

		labels: labels{Os: Windows},
	},
	{
		configKeySuffix:  "check-crc-users-group-exists",
		checkDescription: "Checking if crc-users group exists",
		check: func() error {
			if _, _, err := powershell.Execute("Get-LocalGroup -Name crc-users"); err != nil {
				return fmt.Errorf("'crc-users' group does not exist: %v", err)
			}
			return nil
		},
		fixDescription: "Creating crc-users group",
		fix: func() error {
			if _, _, err := powershell.ExecuteAsAdmin("create crc-users group", "New-LocalGroup -Name crc-users"); err != nil {
				return fmt.Errorf("failed to create 'crc-users' group: %v", err)
			}
			return nil
		},
		cleanupDescription: "Removing 'crc-users' group",
		cleanup: func() error {
			if _, _, err := powershell.ExecuteAsAdmin("remove crc-users group", "Remove-LocalGroup -Name crc-users"); err != nil {
				return fmt.Errorf("failed to remove 'crc-users' group: %v", err)
			}
			return nil
		},
		flags:  NoFix | StartUpOnly,
		labels: labels{Os: Windows},
	},
	{
		configKeySuffix:  "check-user-in-hyperv-group",
		checkDescription: "Checking if current user is in Hyper-V Admins group",
		check:            checkIfUserPartOfHyperVAdmins,
		fixDescription:   "Adding current user to Hyper-V Admins group",
		fix:              fixUserPartOfHyperVAdmins,

		labels: labels{Os: Windows},
	},
	{
		configKeySuffix:  "check-hyperv-service-running",
		checkDescription: "Checking if Hyper-V service is enabled",
		check:            checkHyperVServiceRunning,
		fixDescription:   "Enabling Hyper-V service",
		fix:              fixHyperVServiceRunning,
		flags:            StartUpOnly,

		labels: labels{Os: Windows},
	},
	{
		configKeySuffix:  "check-hyperv-switch",
		checkDescription: "Checking if the Hyper-V virtual switch exists",
		check:            checkIfHyperVVirtualSwitchExists,
		fixDescription:   "Unable to perform Hyper-V administrative commands. Please reboot your system and run 'crc setup' to complete the setup process",
		flags:            NoFix | StartUpOnly,

		labels: labels{Os: Windows},
	},
	{
		cleanupDescription: "Removing dns server from interface",
		cleanup:            removeDNSServerAddress,
		flags:              CleanUpOnly,

		labels: labels{Os: Windows},
	},
	{
		cleanupDescription: "Removing the crc VM if exists",
		cleanup:            removeCrcVM,
		flags:              CleanUpOnly,

		labels: labels{Os: Windows},
	},
}

var vsockChecks = []Check{
	{
		configKeySuffix:    "check-vsock",
		checkDescription:   "Checking if vsock is correctly configured",
		check:              checkVsock,
		fixDescription:     "Checking if vsock is correctly configured",
		fix:                fixVsock,
		cleanupDescription: "Removing vsock service from hyperv registry",
		cleanup:            cleanVsock,
		flags:              NoFix | StartUpOnly,

		labels: labels{Os: Windows, NetworkMode: User},
	},
}

var errReboot = errors.New("Please reboot your system and run 'crc setup' to complete the setup process")

func username() string {
	if ok, _ := win32.DomainJoined(); ok {
		return fmt.Sprintf(`%s\%s`, os.Getenv("USERDOMAIN"), os.Getenv("USERNAME"))
	}
	return os.Getenv("USERNAME")
}

const (
	// This key is required to activate the vsock communication
	registryDirectory = `HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices`
	// First part of the key is the vsock port. The rest is not used and just a placeholder.
	registryKey   = "00000400-FACB-11E6-BD58-64006A7986D3"
	registryValue = "gvisor-tap-vsock"
)

func checkVsock() error {
	stdout, _, err := powershell.Execute(fmt.Sprintf(`Get-Item -Path "%s\%s"`, registryDirectory, registryKey))
	if err != nil {
		return err
	}
	if !strings.Contains(stdout, registryValue) {
		return errors.New("VSock registry key not correctly configured")
	}
	return nil
}

func fixVsock() error {
	cmds := []string{
		fmt.Sprintf(`$service = New-Item -Path "%s" -Name "%s"`, registryDirectory, registryKey),
		fmt.Sprintf(`$service.SetValue("ElementName", "%v")`, registryValue),
	}
	_, _, err := powershell.ExecuteAsAdmin("adding vsock registry key", strings.Join(cmds, ";"))
	return err
}

func cleanVsock() error {
	if err := checkVsock(); err != nil {
		return nil
	}
	_, _, err := powershell.ExecuteAsAdmin("Removing vsock registry key", fmt.Sprintf(`Remove-Item -Path "%s\%s"`, registryDirectory, registryKey))
	if err != nil {
		return fmt.Errorf("Unable to remove vsock service from hyperv registry: %v", err)
	}
	return nil
}

// We want all preflight checks including
// - experimental checks
// - tray checks when using an installer, regardless of tray enabled or not
// - both user and system networking checks
//
// Passing 'UserNetworkingMode' to getPreflightChecks currently achieves this
// as there are no system networking specific checks
func getAllPreflightChecks() []Check {
	return getPreflightChecks(true, true, network.UserNetworkingMode)
}

func getChecks() []Check {
	checks := []Check{}
	checks = append(checks, hypervPreflightChecks...)
	checks = append(checks, vsockChecks...)
	checks = append(checks, bundleCheck)
	checks = append(checks, genericCleanupChecks...)
	return checks
}

func getPreflightChecks(_ bool, trayAutoStart bool, networkMode network.Mode) []Check {
	filter := newFilter()
	filter.SetNetworkMode(networkMode)

	return filter.Apply(getChecks())
}
