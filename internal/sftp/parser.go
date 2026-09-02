package sftp

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Item struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"isDir"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
}

// ParseLsLf parses `ls -la` output. The leading "total N" line is skipped,
// and the "." / ".." pseudo-entries (present with -a) are dropped — the
// frontend renders its own ".." navigation row.
// Format per entry: mode links owner group size MMM DD [HH:MM|YYYY] name
func ParseLsLf(output string) ([]Item, error) {
	var items []Item
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "total ") || strings.HasPrefix(line, "sftp> ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mode := fields[0]
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		// fields[5] onward = "MMM DD time|year name" (name may contain spaces)
		rest := strings.TrimSpace(strings.Join(fields[5:], " "))
		name := nameAfterDate(rest)
		if name == "." || name == ".." {
			continue
		}
		items = append(items, Item{
			Name:    name,
			Size:    size,
			IsDir:   strings.HasPrefix(mode, "d"),
			Mode:    mode,
			ModTime: parseModTime(rest),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// An empty/error-only listing is a valid empty directory, not an error.
	return items, nil
}

// nameAfterDate strips the leading "MMM DD time|year" tokens from rest.
func nameAfterDate(rest string) string {
	parts := strings.Split(rest, " ")
	if len(parts) < 4 {
		return strings.Join(parts, " ")
	}
	// parts[0]=MMM, parts[1]=DD, parts[2]=time|YYYY, parts[3:]=name
	return strings.TrimSpace(strings.Join(parts[3:], " "))
}

var monthNum = map[string]int{
	"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6,
	"Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12,
}

// parseModTime converts sftp's `ls -l` date field ("MMM DD [HH:MM|YYYY]",
// without the trailing filename) into the canonical "YYYY-MM-DD HH:MM" form.
// A time token means this year; a bare year token means that year (old files).
func parseModTime(rest string) string {
	parts := strings.Split(rest, " ")
	if len(parts) < 3 {
		return ""
	}
	mon := monthNum[parts[0]]
	if mon == 0 {
		return ""
	}
	day, _ := strconv.Atoi(parts[1])
	third := parts[2] // "HH:MM" or "YYYY"
	year := 0
	clock := ""
	if strings.Contains(third, ":") {
		year = time.Now().Year()
		clock = third
	} else {
		year, _ = strconv.Atoi(third)
	}
	if year == 0 || day == 0 {
		return ""
	}
	date := fmt.Sprintf("%04d-%02d-%02d", year, mon, day)
	if clock != "" {
		return date + " " + clock
	}
	return date
}
