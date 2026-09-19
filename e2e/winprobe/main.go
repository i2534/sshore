// winprobe 是 Task 0 的 Windows 真机探测程序（原 probe2.exe），用于复核
// docs/windows-acceptance-checklist.md §2 的四项判定边界。它只依赖系统 ssh 与
// github.com/pkg/sftp，不依赖 sshore 本身；客户机没有 Go 工具链时，在宿主交叉构建后
// scp 投放（见清单 §2 的命令）。
//
// 判定项与 Task 0 报告 / probe.md 的对应关系：
//   - -s 前置/后置：两种写法都要能起子系统；
//   - posix-rename@openssh.com：扩展被服务端通告（Task 0 证据弱，仅一行 ext/value）；
//   - plain Rename 到已存在目标按期望失败、PosixRename 真覆盖（证据强）；
//   - Seek(3)/Seek(5)/Seek(7) 续写：文件内部回填、恰好末尾、越过 EOF（空洞补 0）；
//   - 不存在的子系统请求：ssh 会把含 "subsystem request failed" 的 stderr 打出来。
//
// 用法（客户机 PowerShell；参数可省，默认值即 Task 0 那台机器的形态）：
//
//	winprobe.exe -host 127.0.0.1 -port 22 -key C:/Users/lan/.ssh/id_winlocal -base C:/Users/lan/winprobe/data
//
// 注意 -port 是客户机自己 sshd 的监听端口（自连 127.0.0.1）；从宿主经 NAT 转发访问时
// 用的是另一侧的 2222，别混。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/pkg/sftp"
)

type config struct {
	host string
	port string
	key  string
	base string
}

// sshArgs 组装自连参数。key 为空时不传 -i（依赖客户机的默认身份）。
func (c config) sshArgs(extra ...string) []string {
	a := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=NUL", "-p", c.port}
	if c.key != "" {
		a = append(a, "-i", c.key)
	}
	return append(a, extra...)
}

// dialArgs 返回指定 -s 写法的完整参数：前置 = ssh -s <host> sftp；后置 = ssh <host> -s sftp。
func (c config) dialArgs(pre bool) []string {
	if pre {
		return c.sshArgs("-s", c.host, "sftp")
	}
	return c.sshArgs(c.host, "-s", "sftp")
}

func dial(c config, pre bool) (*sftp.Client, *exec.Cmd, error) {
	cmd := exec.Command("ssh", c.dialArgs(pre)...)
	w, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	r, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	cl, err := sftp.NewClientPipe(r, w)
	return cl, cmd, err
}

func writeFile(cl *sftp.Client, p string, b []byte) error {
	f, err := cl.Create(p)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func readFile(cl *sftp.Client, p string) ([]byte, error) {
	f, err := cl.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func sz(fi os.FileInfo) int64 {
	if fi == nil {
		return -1
	}
	return fi.Size()
}

// checkSubsystemOrder 复核 §2.1：-s 前置与后置两种写法都要能建立子系统。
func checkSubsystemOrder(c config) {
	for _, mode := range []struct {
		name string
		pre  bool
	}{{"pre", true}, {"post", false}} {
		cl, cmd, err := dial(c, mode.pre)
		if err != nil {
			fmt.Printf("[%s] NewClientPipe err: %v\n", mode.name, err)
			if cmd != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
			continue
		}
		wd, _ := cl.Getwd()
		fmt.Printf("[%s] client ok, wd=%s\n", mode.name, wd)
		_ = cl.Close()
		if cmd != nil {
			_ = cmd.Wait()
		}
	}
}

// seekCase 复核 §2.3 的单条边界。原文件恒为 init，Seek(off) 后续写 suffix，
// 读回内容与 Stat 大小都要打印出来。
func seekCase(cl *sftp.Client, dir, name string, init []byte, off int64, suffix []byte) {
	p := dir + "/" + name
	if err := writeFile(cl, p, init); err != nil {
		fmt.Printf("seek[%s] write init err: %v\n", name, err)
		return
	}
	f, err := cl.OpenFile(p, os.O_WRONLY)
	if err != nil {
		fmt.Printf("seek[%s] open err: %v\n", name, err)
		return
	}
	pos, seekErr := f.Seek(off, 0)
	n, writeErr := f.Write(suffix)
	closeErr := f.Close()
	b, readErr := readFile(cl, p)
	st, statErr := cl.Stat(p)
	fmt.Printf("seek[%s off=%d] init=%q suffix=%q seekRet=%d seekErr=%v writeN=%d writeErr=%v closeErr=%v statErr=%v statSize=%d readErr=%v content=%q hex=% x\n",
		name, off, string(init), string(suffix), pos, seekErr, n, writeErr, closeErr, statErr, sz(st), readErr, string(b), b)
}

func checkRenameAndSeek(c config) {
	cl, cmd, err := dial(c, true)
	if err != nil {
		fmt.Printf("dial err: %v\n", err)
		return
	}
	defer func() {
		_ = cl.Close()
		if cmd != nil {
			_ = cmd.Wait()
		}
	}()

	ext, has := cl.HasExtension("posix-rename@openssh.com")
	fmt.Printf("HasExtension(posix-rename@openssh.com) -> value=%q has=%v\n", ext, has)

	dir := fmt.Sprintf("%s/run-%d", c.base, time.Now().UnixNano())
	if err := cl.MkdirAll(dir); err != nil {
		fmt.Printf("MkdirAll err: %v\n", err)
		return
	}
	fmt.Printf("dir=%s\n", dir)
	src := dir + "/src.txt"
	old := dir + "/old.txt"
	if err := writeFile(cl, src, []byte("NEW")); err != nil {
		fmt.Printf("write src err: %v\n", err)
	}
	if err := writeFile(cl, old, []byte("OLD")); err != nil {
		fmt.Printf("write old err: %v\n", err)
	}
	// §2.2：plain Rename 到已存在目标按期望失败（SSH_FX_FAILURE），目标内容不变。
	fmt.Printf("plain Rename over existing -> %v\n", cl.Rename(src, old))
	if b, rerr := readFile(cl, old); rerr == nil {
		st, _ := cl.Stat(old)
		fmt.Printf("after plain rename, old.txt=%q statSize=%d\n", string(b), sz(st))
	} else {
		fmt.Printf("after plain rename read err: %v\n", rerr)
	}
	// §2.2：PosixRename 真覆盖：返回 <nil>、读回新内容、磁盘大小同步。
	if err := writeFile(cl, src, []byte("NEW2")); err != nil {
		fmt.Printf("write src2 err: %v\n", err)
	}
	fmt.Printf("PosixRename over existing -> %v\n", cl.PosixRename(src, old))
	if b, rerr := readFile(cl, old); rerr == nil {
		st, _ := cl.Stat(old)
		fmt.Printf("after PosixRename, old.txt=%q statSize=%d\n", string(b), sz(st))
	} else {
		fmt.Printf("after PosixRename read err: %v\n", rerr)
	}

	// §2.3：初始恒为 5 字节 ABCDE。Seek(3) 内部回填 -> ABCXY；
	// Seek(5) 恰好末尾 -> ABCDEYZ；Seek(7) 越过 EOF -> ABCDE + 两个 0x00 + XY（空洞补 0）。
	seekCase(cl, dir, "seek3.txt", []byte("ABCDE"), 3, []byte("XY"))
	seekCase(cl, dir, "seek5.txt", []byte("ABCDE"), 5, []byte("YZ"))
	seekCase(cl, dir, "seek7.txt", []byte("ABCDE"), 7, []byte("XY"))
}

// checkStderr 复核 §2.4：请求不存在的子系统时 ssh 会写 stderr（含 subsystem request failed），
// 应用侧必须能看到这段原文，且不能因为管道无人读而卡住。
func checkStderr(c config) {
	{
		cmd := exec.Command("ssh", c.sshArgs("-s", c.host, "nosuchsubsystem")...)
		var buf bytes.Buffer
		cmd.Stderr = &buf
		err := cmd.Run()
		fmt.Printf("[stderr-A/captured] ssh -s %s nosuchsubsystem -> runErr=%v stderrBytes=%d\n", c.host, err, buf.Len())
		fmt.Println("[stderr-A/captured] stderr text >>>")
		fmt.Print(buf.String())
		fmt.Println("<<<")
	}
	{
		// 调用方不接管 stderr：进程仍要结束（证明 drain/丢弃不会卡住）。
		cmd := exec.Command("ssh", c.sshArgs("-s", c.host, "nosuchsubsystem")...)
		err := cmd.Run()
		fmt.Printf("[stderr-B/unset] ssh -s %s nosuchsubsystem -> runErr=%v\n", c.host, err)
	}
}

func main() {
	var c config
	flag.StringVar(&c.host, "host", "127.0.0.1", "客户机自身 sshd 的主机")
	flag.StringVar(&c.port, "port", "22", "客户机自身 sshd 的端口（自连，不是 NAT 的 2222）")
	flag.StringVar(&c.key, "key", "C:/Users/lan/.ssh/id_winlocal", "自连私钥；留空则用默认身份")
	flag.StringVar(&c.base, "base", "/C:/Users/lan/winprobe/data", "远端探测数据目录（自动创建）")
	flag.Parse()

	fmt.Printf("=== winprobe pid=%d at %s host=%s port=%s key=%s base=%s ===\n",
		os.Getpid(), time.Now().Format(time.RFC3339), c.host, c.port, c.key, c.base)
	checkSubsystemOrder(c)
	checkRenameAndSeek(c)
	checkStderr(c)
	fmt.Println("=== winprobe done ===")
}
