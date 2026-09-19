package update

// lockFileName 是 ExeDir 下的排他锁文件名。
//
// 它只是"锁的载体"，不用来传递任何状态；锁本身由内核对象承载：
// Unix 是 flock(2) 的文件锁，Windows 是命名互斥体。因此进程崩溃退出、
// 被 kill、或升级脚本以任何方式终止时，锁都由内核自动释放，
// 不会留下需要人工清理的死锁状态，脚本也不必参与加锁/解锁。
//
// 两个实例同时 apply 时会在 ExeDir 内互相 rename，可能把正在替换的
// 二进制覆盖掉（spec §7.6），所以 apply 前必须先取到这把锁。
const lockFileName = ".sshore-update.lock"
