package main

import (
	"debug/elf"
	"encoding/binary"
	"io"
	"os"
	"runtime"
)

// cliFileLooksRunnable is the file test of a cache hit with the managed
// launcher gate on. A CLI file that fails it does not run, and the install
// runs over it. The file must be a regular file with an exec bit that starts
// with "#!" or with an ELF header for this machine. On 5fd08069 the new CLI
// replaces a file with the text "hello" and an arm64 ELF on amd64. The launcher
// runs only the new CLI (cells L14a and L14b, Linux VM). The test beyond those
// two files is claustrum's own.
func cliFileLooksRunnable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 20)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if len(head) >= 2 && head[0] == '#' && head[1] == '!' {
		return true
	}
	return elfForThisMachine(head)
}

// elfForThisMachine reports whether head starts an ELF header whose machine is
// that of GOARCH. For a GOARCH outside the list any ELF header passes
// (claustrum's own).
func elfForThisMachine(head []byte) bool {
	if len(head) < 20 || string(head[:4]) != elf.ELFMAG {
		return false
	}
	var order binary.ByteOrder = binary.LittleEndian
	if elf.Data(head[elf.EI_DATA]) == elf.ELFDATA2MSB {
		order = binary.BigEndian
	}
	machine := elf.Machine(order.Uint16(head[18:20]))
	want, known := map[string]elf.Machine{
		"amd64": elf.EM_X86_64,
		"arm64": elf.EM_AARCH64,
		"386":   elf.EM_386,
		"arm":   elf.EM_ARM,
	}[runtime.GOARCH]
	return !known || machine == want
}
