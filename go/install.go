package leaninteract

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// InstallLean installs elan, the Lean toolchain manager, with the official
// installer scripts (https://github.com/leanprover/elan), and on Unix adds
// ~/.elan/bin to PATH in existing shell rc files.
func InstallLean() error {
	Logger.Info("detected operating system", "os", runtime.GOOS)
	run := func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		return cmd.Run()
	}
	var err error
	switch runtime.GOOS {
	case "windows":
		CheckWindowsLongPaths()
		Logger.Info("installing elan for Windows...")
		if err = run("curl", "-O", "--location", "https://raw.githubusercontent.com/leanprover/elan/master/elan-init.ps1"); err == nil {
			err = run("powershell", "-ExecutionPolicy", "Bypass", "-Command", "& './elan-init.ps1' -NoPrompt $true -DefaultToolchain stable")
			os.Remove("elan-init.ps1")
		}
		if err == nil {
			Logger.Info("Elan has been installed. You may need to restart your terminal for the PATH changes to take effect.")
		}
	case "linux", "darwin":
		err = run("sh", "-c", "curl https://raw.githubusercontent.com/leanprover/elan/master/elan-init.sh -sSf | sh -s -- -y --default-toolchain stable")
		if err == nil {
			home, _ := os.UserHomeDir()
			for _, name := range []string{".bashrc", ".zshrc", ".bash_profile", ".profile"} {
				path := filepath.Join(home, name)
				if _, statErr := os.Stat(path); statErr != nil {
					continue
				}
				f, ferr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
				if ferr != nil {
					Logger.Warn("could not modify shell config", "path", path, "err", ferr)
					continue
				}
				_, _ = f.WriteString("\nexport PATH=\"$HOME/.elan/bin:$PATH\"\n")
				f.Close()
				Logger.Info("added elan to PATH", "path", path)
			}
			Logger.Info("Please restart your terminal or run 'source ~/.profile' to update your PATH")
		}
	default:
		return fmt.Errorf("leaninteract: unsupported operating system %s; please install elan manually: https://leanprover-community.github.io/get_started.html", runtime.GOOS)
	}
	if err != nil {
		Logger.Warn("an error occurred during Lean installation; see https://leanprover-community.github.io/get_started.html", "err", err)
		return err
	}
	Logger.Info("Lean installation completed successfully.")
	return nil
}

// CheckWindowsLongPaths logs advice when Windows long paths or git
// core.longpaths are not enabled. It does nothing on other systems.
func CheckWindowsLongPaths() {
	if runtime.GOOS != "windows" {
		return
	}
	out, err := exec.Command("reg", "query", `HKLM\SYSTEM\CurrentControlSet\Control\FileSystem`, "/v", "LongPathsEnabled").Output()
	switch {
	case err != nil:
		Logger.Warn("could not check Windows long path setting", "err", err)
	case strings.Contains(string(out), "0x1"):
		Logger.Info("Windows long paths already enabled")
	default:
		Logger.Info("For optimal use on Windows, enable long paths by running this command as administrator:")
		Logger.Info(`New-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem" -Name LongPathsEnabled -Value 1 -PropertyType DWord -Force`)
	}
	gitOut, err := exec.Command("git", "config", "--get", "core.longpaths").Output()
	if err == nil && strings.TrimSpace(string(gitOut)) == "true" {
		Logger.Info("Git already configured for long paths")
	} else {
		Logger.Info("For optimal use on Windows, configure git for long paths by running:")
		Logger.Info("git config --global core.longpaths true")
	}
}
