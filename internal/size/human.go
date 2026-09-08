package size

import "fmt"

func Human(b int64) string {
	const (
		kb = 1024
		mb = kb * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/GB)
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/mb)
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/kb)
	}
	return fmt.Sprintf("%d B", b)
}

func HumanDelta(b int64) string {
	if b < 0 {
		return "-" + Human(-b)
	}
	return "+" + Human(b)
}
