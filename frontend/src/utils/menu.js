// 子菜单定位纯函数：只做算术、不访问 DOM，因此能在无 jsdom 的环境里被真单测。
//
// 调用方传入的 anchor 是父项 getBoundingClientRect() 的四个值，panel 是子面板测量出的
// 尺寸，viewport 取 window.innerWidth / innerHeight（子面板用 position: fixed）。
export function submenuPlacement(anchor, panel, viewport, gap = 2) {
  // ① 水平：优先贴父项右侧；右侧放不下就左翻到父项左侧；左翻仍越界则贴左边缘。
  let left = anchor.right + gap
  if (left + panel.width > viewport.width) {
    left = anchor.left - gap - panel.width
    if (left < 0) left = 0
  }

  // ② 垂直：贴父项顶；下方放不下就上移；上移仍越界则贴顶。
  let top = anchor.top
  if (top + panel.height > viewport.height) {
    top = viewport.height - panel.height
    if (top < 0) top = 0
  }

  // ③ maxHeight 保证面板不越过下边缘（面板自身仍需 overflow: auto）。
  return { left, top, maxHeight: viewport.height - top }
}
