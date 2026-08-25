// Package terminfo generates the compiled ncurses entry used by st-go.
package terminfo

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

const (
	Name  = "st-256color"
	Alias = "stterm-256color"

	magic32 = 0x021e
	absent  = -1
)

// Generate returns a compiled ncurses terminfo entry matching st-go's parser
// and default key map. The dimensions and tab width describe the configured
// initial screen; zero values use the traditional 80x24, 8-column defaults.
func Generate(cols, lines, tabWidth uint) []byte {
	if cols == 0 {
		cols = 80
	}
	if lines == 0 {
		lines = 24
	}
	if tabWidth == 0 {
		tabWidth = 8
	}

	names := []byte(Name + "|" + Alias + "|simpleterm with 256 colors\x00")
	booleans := make([]byte, 29)
	for _, i := range []int{1, 4, 9, 13, 14, 25, 27, 28} { // am, xenl, hs, mir, msgr, npc, ccc, bce
		booleans[i] = 1
	}
	numbers := []int32{
		int32(cols), int32(tabWidth), int32(lines), absent, absent,
		absent, absent, absent, absent, absent, absent, absent, absent,
		256, 65536,
	}

	strings := standardStrings()
	stringOffsets, stringTable := makeStringTable(strings)
	extBoolNames := []string{"XT"}
	extStrings := []namedString{
		{"BD", "\x1b[?2004l"},
		{"BE", "\x1b[?2004h"},
		{"Ms", "\x1b]52;%p1%s;%p2%s\a"},
		{"PE", "\x1b[201~"},
		{"PS", "\x1b[200~"},
		{"Se", "\x1b[2 q"},
		{"Ss", "\x1b[%p1%d q"},
		{"TS", "\x1b]0;"},
		{"kDN3", "\x1b[1;3B"},
		{"kDN5", "\x1b[1;5B"},
		{"kLFT3", "\x1b[1;3D"},
		{"kLFT5", "\x1b[1;5D"},
		{"kNXT3", "\x1b[6;3~"},
		{"kNXT5", "\x1b[6;5~"},
		{"kPRV3", "\x1b[5;3~"},
		{"kPRV5", "\x1b[5;5~"},
		{"kRIT3", "\x1b[1;3C"},
		{"kRIT5", "\x1b[1;5C"},
		{"kUP3", "\x1b[1;3A"},
		{"kUP5", "\x1b[1;5A"},
		{"rmxx", "\x1b[29m"},
		{"smxx", "\x1b[9m"},
	}
	extOffsets, extTable := makeExtendedTable(extBoolNames, extStrings)

	var out bytes.Buffer
	write16 := func(v int) { _ = binary.Write(&out, binary.LittleEndian, int16(v)) }
	write16(magic32)
	write16(len(names))
	write16(len(booleans))
	write16(len(numbers))
	write16(len(strings))
	write16(len(stringTable))
	out.Write(names)
	out.Write(booleans)
	padEven(&out)
	for _, n := range numbers {
		_ = binary.Write(&out, binary.LittleEndian, n)
	}
	for _, offset := range stringOffsets {
		write16(offset)
	}
	out.Write(stringTable)
	padEven(&out)

	write16(len(extBoolNames))
	write16(0) // extended numbers
	write16(len(extStrings))
	write16(len(extOffsets))
	write16(len(extTable))
	for range extBoolNames {
		out.WriteByte(1)
	}
	padEven(&out)
	for _, offset := range extOffsets {
		write16(offset)
	}
	out.Write(extTable)
	return out.Bytes()
}

// Install writes the generated entry and its alias beneath root. When root is
// empty, ncurses' TERMINFO directory or ~/.terminfo is used.
func Install(root string, cols, lines, tabWidth uint) (string, error) {
	if root == "" {
		root = os.Getenv("TERMINFO")
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("terminfo home: %w", err)
		}
		root = filepath.Join(home, ".terminfo")
	}
	dir := filepath.Join(root, Name[:1])
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create terminfo directory: %w", err)
	}
	path := filepath.Join(dir, Name)
	data := Generate(cols, lines, tabWidth)
	if err := writeAtomic(path, data); err != nil {
		return "", err
	}
	alias := filepath.Join(dir, Alias)
	if err := os.Remove(alias); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("replace terminfo alias: %w", err)
	}
	if err := os.Link(path, alias); err != nil {
		if err := writeAtomic(alias, data); err != nil {
			return "", err
		}
	}
	return path, nil
}

type namedString struct {
	name  string
	value string
}

func standardStrings() []string {
	strings := make([]string, 361)
	set := func(index int, value string) { strings[index] = value }
	set(0, "\x1b[Z")
	set(1, "\a")
	set(2, "\r")
	set(3, "\x1b[%i%p1%d;%p2%dr")
	set(4, "\x1b[3g")
	set(5, "\x1b[H\x1b[2J")
	set(6, "\x1b[K")
	set(7, "\x1b[J")
	set(8, "\x1b[%i%p1%dG")
	set(10, "\x1b[%i%p1%d;%p2%dH")
	set(11, "\n")
	set(12, "\x1b[H")
	set(13, "\x1b[?25l")
	set(14, "\b")
	set(16, "\x1b[?25h")
	set(17, "\x1b[C")
	set(19, "\x1b[A")
	set(21, "\x1b[P")
	set(22, "\x1b[M")
	set(23, "\x1b]0;\a")
	set(25, "\x1b(0")
	set(26, "\x1b[5m")
	set(27, "\x1b[1m")
	set(28, "\x1b[?1049h")
	set(30, "\x1b[2m")
	set(31, "\x1b[4h")
	set(32, "\x1b[8m")
	set(34, "\x1b[7m")
	set(35, "\x1b[7m")
	set(36, "\x1b[4m")
	set(37, "\x1b[%p1%dX")
	set(38, "\x1b(B")
	set(39, "\x1b[0m")
	set(40, "\x1b[?1049l")
	set(42, "\x1b[4l")
	set(43, "\x1b[27m")
	set(44, "\x1b[24m")
	set(45, "\x1b[?5h$<100/>\x1b[?5l")
	set(47, "\a")
	set(49, "\x1b[4l\x1b>\x1b[?1034l")
	set(53, "\x1b[L")
	set(55, "\x7f")
	set(57, "\x1b[3;5~")
	set(59, "\x1b[3~")
	set(60, "\x1b[3;2~")
	set(61, "\x1bOB")
	set(62, "\x1b[2;2~")
	set(63, "\x1b[1;2F")
	set(64, "\x1b[1;5F")
	for n := 1; n <= 10; n++ {
		index := 66
		if n == 10 {
			index = 67
		} else if n > 1 {
			index = 66 + n
		}
		set(index, functionKey(n))
	}
	set(76, "\x1b[1~")
	set(77, "\x1b[2~")
	set(78, "\x1b[2;5~")
	set(79, "\x1bOD")
	set(81, "\x1b[6~")
	set(82, "\x1b[5~")
	set(83, "\x1bOC")
	set(84, "\x1b[1;2B")
	set(85, "\x1b[1;2A")
	set(87, "\x1bOA")
	set(88, "\x1b[?1l\x1b>")
	set(89, "\x1b[?1h\x1b=")
	set(105, "\x1b[%p1%dP")
	set(106, "\x1b[%p1%dM")
	set(107, "\x1b[%p1%dB")
	set(108, "\x1b[%p1%d@")
	set(109, "\x1b[%p1%dS")
	set(110, "\x1b[%p1%dL")
	set(111, "\x1b[%p1%dD")
	set(112, "\x1b[%p1%dC")
	set(113, "\x1b[%p1%dT")
	set(114, "\x1b[%p1%dA")
	set(118, "\x1b[i")
	set(119, "\x1b[4i")
	set(120, "\x1b[5i")
	set(122, "\x1bc")
	set(123, "\x1b[4l\x1b>\x1b[?1034l")
	set(126, "\x1b8")
	set(127, "\x1b[%i%p1%dd")
	set(128, "\x1b7")
	set(129, "\n")
	set(130, "\x1bM")
	set(131, "%?%p9%t\x1b(0%e\x1b(B%;\x1b[0%?%p6%t;1%;%?%p2%t;4%;%?%p1%p3%|%t;7%;%?%p4%t;5%;%?%p5%t;2%;%?%p7%t;8%;m")
	set(132, "\x1bH")
	set(134, "\t")
	set(135, "\x1b]0;")
	set(139, "\x1b[1~")
	set(140, "\x1b[5~")
	set(141, "\x1bOu")
	set(142, "\x1b[4~")
	set(143, "\x1b[6~")
	set(146, "+C,D-A.B0E``aaffgghFiGjjkkllmmnnooppqqrrssttuuvvwwxxyyzz{{||}}~~")
	set(155, "\x1b)0")
	set(164, "\x1b[4~")
	set(191, "\x1b[3;2~")
	set(194, "\x1b[1;2F")
	set(199, "\x1b[1;2H")
	set(200, "\x1b[2;2~")
	set(201, "\x1b[1;2D")
	set(204, "\x1b[6;2~")
	set(206, "\x1b[5;2~")
	set(210, "\x1b[1;2C")
	for n := 11; n <= 63; n++ {
		set(205+n, functionKey(n))
	}
	set(269, "\x1b[1K")
	set(293, "\x1b[%i%d;%dR")
	set(294, "\x1b[6n")
	set(295, "\x1b[?1;2c")
	set(296, "\x1b[c")
	set(297, "\x1b[39;49m")
	set(298, "\x1b]104\a")
	set(299, "\x1b]4;%p1%d;rgb:%p2%{255}%*%{1000}%/%2.2X/%p3%{255}%*%{1000}%/%2.2X/%p4%{255}%*%{1000}%/%2.2X\x1b\\")
	set(311, "\x1b[3m")
	set(321, "\x1b[23m")
	set(355, "\x1b[M")
	set(359, "\x1b[%?%p1%{8}%<%t3%p1%d%e%p1%{16}%<%t9%p1%{8}%-%d%e38;5;%p1%d%;m")
	set(360, "\x1b[%?%p1%{8}%<%t4%p1%d%e%p1%{16}%<%t10%p1%{8}%-%d%e48;5;%p1%d%;m")
	return strings
}

func functionKey(n int) string {
	bases := []string{"P", "Q", "R", "S", "15~", "17~", "18~", "19~", "20~", "21~", "23~", "24~"}
	physical := (n - 1) % 12
	if n <= 12 {
		if physical < 4 {
			return "\x1bO" + bases[physical]
		}
		return "\x1b[" + bases[physical]
	}
	params := []int{2, 5, 6, 3, 4}
	group := (n - 13) / 12
	param := params[group]
	base := bases[physical]
	if physical < 4 {
		return fmt.Sprintf("\x1b[1;%d%s", param, base)
	}
	return fmt.Sprintf("\x1b[%s", base[:len(base)-1]) + fmt.Sprintf(";%d~", param)
}

func makeStringTable(values []string) ([]int, []byte) {
	offsets := make([]int, len(values))
	table := make([]byte, 0, 1536)
	for i, value := range values {
		if value == "" {
			offsets[i] = absent
			continue
		}
		offsets[i] = len(table)
		table = append(table, value...)
		table = append(table, 0)
	}
	return offsets, table
}

func makeExtendedTable(boolNames []string, values []namedString) ([]int, []byte) {
	offsets := make([]int, 0, len(values)+len(boolNames)+len(values))
	table := make([]byte, 0, 512)
	for _, item := range values {
		offsets = append(offsets, len(table))
		table = append(table, item.value...)
		table = append(table, 0)
	}
	nameBase := len(table)
	for _, name := range boolNames {
		offsets = append(offsets, len(table)-nameBase)
		table = append(table, name...)
		table = append(table, 0)
	}
	for _, item := range values {
		offsets = append(offsets, len(table)-nameBase)
		table = append(table, item.name...)
		table = append(table, 0)
	}
	return offsets, table
}

func padEven(out *bytes.Buffer) {
	if out.Len()%2 != 0 {
		out.WriteByte(0)
	}
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".st-terminfo-*")
	if err != nil {
		return fmt.Errorf("create terminfo entry: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return fmt.Errorf("set terminfo permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write terminfo entry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close terminfo entry: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install terminfo entry: %w", err)
	}
	return nil
}
