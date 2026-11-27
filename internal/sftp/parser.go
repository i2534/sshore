package sftp

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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
//
// S4：前 5 个字段（mode/links/owner/group/size）与日期段（MMM DD time|year）按空白扫描
// 定位，**不**用 strings.Fields 折叠整行 —— 那会把文件名里的连续空格压成一个、把首尾空格
// 吃掉。名字取「日期段之后第一个空白字符」之后的**原始子串**，逐字保留内部/首尾空格。
// 取舍：ls 输出里名字紧跟日期后的分隔空白，名字自身的首尾空格与分隔符无法区分时，按
// 「日期后第一个空白为分隔、其余保留」处理。字段不足（含恰好 5 字段的错误行，如
// `Can't ls: "/x" not found`）或名字为空串的行一律跳过，绝不产出 Name="" 的条目。
func ParseLsLf(output string) ([]Item, error) {
	var items []Item
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "total ") || strings.HasPrefix(line, "sftp> ") {
			continue
		}
		// 前 5 个字段按空白扫描定位。
		i, ok := 0, true
		var mode string
		var size int64
		for f := 0; f < 5; f++ {
			s, e, found := nextLsField(line, i)
			if !found {
				ok = false
				break
			}
			switch f {
			case 0:
				mode = line[s:e]
			case 4:
				size, _ = strconv.ParseInt(line[s:e], 10, 64)
			}
			i = e
		}
		if !ok {
			continue
		}
		// 日期段 MMM DD time|year：按字段跳过（字段间空白游程整体跳过），记录日期起点与第 3 字段终点。
		dateStart := -1
		for f := 0; f < 3; f++ {
			s, e, found := nextLsField(line, i)
			if !found {
				ok = false
				break
			}
			if f == 0 {
				dateStart = s
			}
			i = e
		}
		if !ok || dateStart < 0 {
			continue
		}
		// 名字 = 日期后第一个空白字符之后的原始子串（保留内部/首尾空格）。
		if i >= len(line) {
			continue
		}
		r, sz := utf8.DecodeRuneInString(line[i:])
		if !unicode.IsSpace(r) {
			continue
		}
		name := line[i+sz:]
		if name == "" || name == "." || name == ".." {
			continue
		}
		items = append(items, Item{
			Name:    name,
			Size:    size,
			IsDir:   strings.HasPrefix(mode, "d"),
			Mode:    mode,
			ModTime: parseModTime(line[dateStart:i]),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// An empty/error-only listing is a valid empty directory, not an error.
	return items, nil
}

// nextLsField 从 line[i:] 起跳过空白游程后定位一个字段，返回 [start,end) 与是否找到。
// 按 rune 解码（与 strings.Fields 的空白语义一致），避免把 UTF-8 续字节误判成空白。
func nextLsField(line string, i int) (start, end int, found bool) {
	for i < len(line) {
		r, sz := utf8.DecodeRuneInString(line[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += sz
	}
	if i >= len(line) {
		return 0, 0, false
	}
	start = i
	for i < len(line) {
		r, sz := utf8.DecodeRuneInString(line[i:])
		if unicode.IsSpace(r) {
			break
		}
		i += sz
	}
	return start, i, true
}

var monthNum = map[string]int{
	"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6,
	"Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12,
}

// parseModTime converts sftp's `ls -l` date field ("MMM DD [HH:MM|YYYY]",
// without the trailing filename) into the canonical "YYYY-MM-DD HH:MM" form.
// A time token means this year; a bare year token means that year (old files).
func parseModTime(rest string) string {
	// rest 现在只含日期段（MMM DD time|year，不含文件名）：用 Fields 容忍日字段右对齐
	// 补空格（"Aug  5"），不会再折叠到名字（名字已在上游按原始子串单独取出）。
	parts := strings.Fields(rest)
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
