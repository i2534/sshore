package sftp

import (
	"bufio"
	"errors"
	"strconv"
	"strings"
)

type Item struct {
	Name    string
	Size    int64
	IsDir   bool
	Mode    string
	ModTime string
}

// ParseLsLf parses `ls -l` output. The leading "total N" line is skipped.
// Format per entry: mode links owner group size MMM DD [HH:MM|YYYY] name
func ParseLsLf(output string) ([]Item, error) {
	var items []Item
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "total ") {
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
		items = append(items, Item{
			Name:    name,
			Size:    size,
			IsDir:   strings.HasPrefix(mode, "d"),
			Mode:    mode,
			ModTime: strings.TrimSpace(strings.Join(fields[5:], " ")),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errors.New("no parseable entries")
	}
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
