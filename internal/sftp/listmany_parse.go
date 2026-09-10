package sftp

import "strings"

// parseListMany 把多命令批处理的输出按命令回显切成块，按**发送顺序**归属到 paths。
//
// 分段依据是回显行前缀 "sftp> "（实测格式形如 `sftp> -ls -la "/x"`）。因此批处理中
// **不能**用 '@' 前缀：它抑制回显，分段依据会丢失。
//
// 关键：失败的块必须判为"未知"并**缺席于返回值**。实测中 `sftp -b` 对失败目录只打印
// `Can't ls: ... not found` 而退出码仍为 0，且 ParseLsLf 会把这种块解析成空列表——
// 若照单全收，就会把"列不出来"误当成"目录是空的"，进而对整棵子树产出 delete 事件。
//
// 归属按索引：第 i 个回显块对应 paths[i]。若块数少于请求数（批处理被截断/回显缺失），
// 多出来的路径一律**缺席**（未知），绝不为它们返回空列表；调用方凭缺失的 key 判断完整性。
func parseListMany(out string, paths []string) map[string][]Item {
	res := make(map[string][]Item, len(paths))
	blocks := splitBlocks(out)
	for i, p := range paths {
		if i >= len(blocks) {
			// 没有对应回显块的路径：未知，缺席于返回值。
			break
		}
		// 安全不变量:去掉回显后内容为空/全空白 ⇒ 列表根本没发生 ⇒ 未知。
		// 实测 OpenSSH sftp 把 "Can't ls" 写到 stderr,失败目录在 stdout 里只剩一行回显;
		// 成功的 ls -la 至少输出 "." 与 "..",所以空块绝不可能是空目录。若把它当场空目录,
		// diff 会把该目录下已知的全部文件判为删除,配合 mirror-delete 会删掉本地文件。
		if strings.TrimSpace(blocks[i]) == "" {
			continue
		}
		if blockFailed(blocks[i]) {
			continue
		}
		items, err := ParseLsLf(blocks[i])
		if err != nil {
			continue
		}
		res[p] = items
	}
	return res
}

// splitBlocks 以 "sftp> " 开头的行作为块边界；首个回显之前的内容（banner）丢弃。
func splitBlocks(out string) []string {
	var blocks []string
	var cur strings.Builder
	started := false
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimRight(line, "\r")
		if strings.HasPrefix(l, "sftp> ") {
			if started {
				blocks = append(blocks, cur.String())
			}
			started = true
			cur.Reset()
			continue
		}
		if started {
			cur.WriteString(l)
			cur.WriteString("\n")
		}
	}
	if started {
		blocks = append(blocks, cur.String())
	}
	return blocks
}

// blockFailed 判断一个输出块是否代表"该目录未知"。sftp 的客户端错误行不带前导空白。
//
// 只用**紧凑前缀**匹配：'not found' 之类的子串检查会误伤文件名里含该字样的正常目录。
// `Can't ls: "..." not found` 本身已被 "Can't " 前缀覆盖。
func blockFailed(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		l := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if l == "" {
			continue
		}
		for _, bad := range []string{"Can't ", "Couldn't ", "Invalid command", "Permission denied"} {
			if strings.HasPrefix(l, bad) {
				return true
			}
		}
	}
	return false
}

// stderrListFailure 在 stderr 中按已请求路径反查客户端错误原文,找不到返回 "".
//
// 实测 OpenSSH sftp 把 `ls` 的客户端错误写到 **stderr**(形如
// `Can't ls: "<path>" not found`,行尾带 CR),stdout 里失败目录只剩回显行。
// 这里用请求路径反查而不是解析任意引号路径:stderr 打印的是 sftp 解析后的真实路径,
// 与请求值逐字相同,可避免路径含引号/反斜杠时的转义歧义。
func stderrListFailure(stderr, path string) string {
	if stderr == "" || path == "" {
		return ""
	}
	for _, line := range strings.Split(stderr, "\n") {
		l := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if l == "" {
			continue
		}
		if strings.Contains(l, "Can't ls: \""+path+"\"") ||
			strings.Contains(l, "Couldn't ls: \""+path+"\"") {
			return l
		}
	}
	return ""
}
