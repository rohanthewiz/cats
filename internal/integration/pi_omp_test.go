package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPiWritesEmbeddedAssetToExtensionsDir(t *testing.T) {
	home := testHome(t)
	extensionsDir := filepath.Join(home, ".pi", "agent", "extensions")
	mustMkdirAll(t, extensionsDir)

	path, err := InstallPi()
	if err != nil {
		t.Fatalf("InstallPi: %v", err)
	}
	if path != filepath.Join(extensionsDir, piExtensionInstallName) {
		t.Fatalf("path = %s", path)
	}
	if mustReadFile(t, path) != piExtensionAsset {
		t.Fatal("written extension differs from embedded asset")
	}
}

func TestInstallPiUsesPiCodingAgentDirEnv(t *testing.T) {
	testHome(t)
	agentDir := t.TempDir()
	mustMkdirAll(t, filepath.Join(agentDir, "extensions"))
	t.Setenv(piCodingAgentDirEnvVar, agentDir)

	path, err := InstallPi()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(agentDir, "extensions", piExtensionInstallName) {
		t.Fatalf("env override not honored: %s", path)
	}
}

func TestInstallPiExpandsTildeInEnvOverride(t *testing.T) {
	home := testHome(t)
	mustMkdirAll(t, filepath.Join(home, "custom-pi", "extensions"))
	t.Setenv(piCodingAgentDirEnvVar, "~/custom-pi")

	path, err := InstallPi()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, "custom-pi", "extensions", piExtensionInstallName) {
		t.Fatalf("tilde not expanded: %s", path)
	}
}

func TestInstallPiErrorsWhenExtensionDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallPi()
	if err == nil || !strings.Contains(err.Error(), "pi extension directory not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestUninstallPiRemovesExtensionWhenPresent(t *testing.T) {
	home := testHome(t)
	extensionPath := filepath.Join(home, ".pi", "agent", "extensions", piExtensionInstallName)
	mustWriteFile(t, extensionPath, piExtensionAsset)

	result, err := UninstallPi()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedExtension || result.ExtensionPath != extensionPath {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(extensionPath); !os.IsNotExist(err) {
		t.Fatal("extension still present")
	}

	// Second uninstall reports nothing removed.
	result, err = UninstallPi()
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedExtension {
		t.Fatal("second uninstall removed something")
	}
}

func TestInstallOmpWritesAssetToOmpExtensionsDir(t *testing.T) {
	home := testHome(t)
	extensionsDir := filepath.Join(home, ".omp", "agent", "extensions")
	mustMkdirAll(t, extensionsDir)

	installed, err := InstallOmp()
	if err != nil {
		t.Fatalf("InstallOmp: %v", err)
	}
	if installed.ExtensionPath != filepath.Join(extensionsDir, ompExtensionInstallName) {
		t.Fatalf("path = %s", installed.ExtensionPath)
	}
	if installed.RemovedLegacyPiExtension {
		t.Fatal("no legacy extension existed")
	}
	if mustReadFile(t, installed.ExtensionPath) != ompExtensionAsset {
		t.Fatal("written extension differs from embedded asset")
	}
}

func TestInstallOmpCreatesExtensionsDirWhenAgentDirExists(t *testing.T) {
	home := testHome(t)
	mustMkdirAll(t, filepath.Join(home, ".omp", "agent"))

	installed, err := InstallOmp()
	if err != nil {
		t.Fatal(err)
	}
	if !isFile(installed.ExtensionPath) {
		t.Fatal("extension not written")
	}
}

func TestInstallOmpRemovesLegacyPiIntegration(t *testing.T) {
	home := testHome(t)
	extensionsDir := filepath.Join(home, ".omp", "agent", "extensions")
	legacyPath := filepath.Join(extensionsDir, piExtensionInstallName)
	mustWriteFile(t, legacyPath, "// HERDR_INTEGRATION_ID=pi\n// HERDR_INTEGRATION_VERSION=1\n")

	installed, err := InstallOmp()
	if err != nil {
		t.Fatal(err)
	}
	if !installed.RemovedLegacyPiExtension {
		t.Fatal("legacy pi extension not removed")
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatal("legacy file still present")
	}
}

func TestInstallOmpPreservesNonHerdrFileWithPiInstallName(t *testing.T) {
	home := testHome(t)
	extensionsDir := filepath.Join(home, ".omp", "agent", "extensions")
	userPath := filepath.Join(extensionsDir, piExtensionInstallName)
	userContent := "// my own extension, not herdr's\nexport {}\n"
	mustWriteFile(t, userPath, userContent)

	installed, err := InstallOmp()
	if err != nil {
		t.Fatal(err)
	}
	if installed.RemovedLegacyPiExtension {
		t.Fatal("user file misidentified as herdr's")
	}
	if mustReadFile(t, userPath) != userContent {
		t.Fatal("user file modified")
	}
}

func TestInstallOmpUsesPiCodingAgentDirEnv(t *testing.T) {
	testHome(t)
	agentDir := t.TempDir()
	mustMkdirAll(t, filepath.Join(agentDir, "extensions"))
	t.Setenv(piCodingAgentDirEnvVar, agentDir)

	installed, err := InstallOmp()
	if err != nil {
		t.Fatal(err)
	}
	if installed.ExtensionPath != filepath.Join(agentDir, "extensions", ompExtensionInstallName) {
		t.Fatalf("env override not honored: %s", installed.ExtensionPath)
	}
}

func TestInstallOmpErrorsWhenExtensionDirMissing(t *testing.T) {
	testHome(t)
	_, err := InstallOmp()
	if err == nil || !strings.Contains(err.Error(), "omp extension directory not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestUninstallOmpRemovesExtensionWhenPresent(t *testing.T) {
	home := testHome(t)
	extensionPath := filepath.Join(home, ".omp", "agent", "extensions", ompExtensionInstallName)
	mustWriteFile(t, extensionPath, ompExtensionAsset)

	result, err := UninstallOmp()
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedExtension {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(extensionPath); !os.IsNotExist(err) {
		t.Fatal("extension still present")
	}
}
