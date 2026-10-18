package main

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// elfHeaderFor is the first 20 bytes of a little-endian 64-bit ELF file for
// machine m.
func elfHeaderFor(m elf.Machine) []byte {
	h := make([]byte, 20)
	copy(h, elf.ELFMAG)
	h[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	h[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	h[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	binary.LittleEndian.PutUint16(h[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(h[18:], uint16(m))
	return h
}

// otherMachine is an ELF machine that is not the one of this test run.
func otherMachine() elf.Machine {
	if runtime.GOARCH == "arm64" {
		return elf.EM_X86_64
	}
	return elf.EM_AARCH64
}

// Rows L14a and L14b of 5fd08069 (Linux VM). With the launcher gate on, a cache
// hit on a CLI file that is no program of this machine runs nothing. The install
// runs over the file, the blob is used, and the launcher runs the new CLI once.
// The facts say cliWasPresent false and launcherStatus usable.
func TestInstallGateReplacesAFileThatIsNoProgram(t *testing.T) {
	for _, c := range []struct {
		id   string
		body []byte
	}{
		{"L14a hello", []byte("hello\n")},
		{"L14b ELF of another machine", elfHeaderFor(otherMachine())},
	} {
		t.Run(c.id, func(t *testing.T) {
			f := newInstallFixture(t)
			if err := os.Remove(f.cli); err != nil {
				t.Fatal(err)
			}
			writeFileMode(t, f.cli, c.body, 0o755)
			blob := filepath.Join(t.TempDir(), "stub.zst")
			writeFileMode(t, blob, zstdOf(t, []byte("#\n")), 0o644)
			f.settings(t, f.wrap)
			t.Setenv(managedLauncherGateEnv, "1")
			// The launcher logs its args and exits 0 without running the CLI.
			helperMode(t, "fail-launcher", "")
			t.Setenv("CLAUSTRUM_TEST_LAUNCH_STDERR", "")
			t.Setenv("CLAUSTRUM_TEST_LAUNCH_EXIT", "0")
			want := `"cliWasPresent":false,"launcherStatus":"usable","launcher":["` + f.wrap + `"],"launcherSource":"` + f.base + `"}` + "\n"
			if got := f.install(t, blob); got != want {
				t.Errorf("facts tail\n got %q\nwant %q", got, want)
			}
			if log, want := readLog(t, f.wrapLog), "LAUNCH "+f.cli+" --version\n"; log != want {
				t.Errorf("launcher log %q, want one run %q", log, want)
			}
			if b, err := os.ReadFile(f.cli); err != nil || string(b) != "#\n" {
				t.Errorf("the CLI file holds %q (err %v), want the new CLI", b, err)
			}
			if isRegularFile(blob) {
				t.Error("the blob was not used")
			}
		})
	}
}

// The file test of the gate. It passes this test binary and a "#!" file with an
// exec bit. It refuses a file with no exec bit, a folder, a text, a short file
// and an ELF header of another machine.
func TestCLIFileLooksRunnable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !cliFileLooksRunnable(exe) {
		t.Errorf("the test binary %s does not pass", exe)
	}
	dir := t.TempDir()
	for _, c := range []struct {
		name string
		body []byte
		mode os.FileMode
		want bool
	}{
		{"script", []byte("#!/x\n"), 0o755, true},
		{"elf-here", elfHeaderFor(map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[runtime.GOARCH]), 0o755, runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"},
		{"no-exec", []byte("#!/x\n"), 0o644, false},
		{"hello", []byte("hello\n"), 0o755, false},
		{"short", []byte("#"), 0o755, false},
		{"elf-other", elfHeaderFor(otherMachine()), 0o755, false},
	} {
		p := filepath.Join(dir, c.name)
		writeFileMode(t, p, c.body, c.mode)
		if got := cliFileLooksRunnable(p); got != c.want {
			t.Errorf("%s: cliFileLooksRunnable = %v, want %v", c.name, got, c.want)
		}
	}
	if cliFileLooksRunnable(dir) {
		t.Error("a folder passes")
	}
	if cliFileLooksRunnable(filepath.Join(dir, "missing")) {
		t.Error("a missing path passes")
	}
}
