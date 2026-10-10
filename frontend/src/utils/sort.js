// 排序切换的纯函数（规格 §7.3）：FilePane 删掉内部 sortKey/sortDir ref 后，
// 「同列取反 / 换列归升序」这条决策只在这里实现一次，由 FilePane.sortBy 调用后 emit 给 SftpView。
// 无副作用：返回新对象，不改 current。

export function nextSort(current, key) {
  const c = current || {}
  // 同列取反；dir 只认 1 作为"当前升序"，其余（含缺省）一律回到升序。
  if (c.key === key) return { key, dir: c.dir === 1 ? -1 : 1 }
  return { key, dir: 1 }
}
