//go:build !linux

package brightness

func Read() (int, int, bool) { return 0, 0, false }
func Change(int) (int, bool) { return 0, false }
