# Gofer 品牌资产

Gofer 的标志表达“接收任务 → 派发执行 → 带回结果”。青色路径代表派发，纸白路径代表返回，琥珀色包裹代表被可靠传递的任务。

## 文件

- `source/gofer-mark.svg`：无文字图形标，以接力轨道和任务包裹表达派发与回传，透明背景。
- `source/gofer-combination.svg`：图形标与小写 `gofer` 的横向组合标；字形已经矢量化，不依赖系统字体。
- `png/`：由 SVG 母版生成的常用尺寸透明 PNG。
- `web/public/favicon.svg`：针对 16–32px 显示单独简化的接力轨道浏览器图标。
- `web/public/favicon.ico`、Apple Touch 和 Android 图标：浏览器与设备入口资产。

重新生成位图：

```powershell
uv run --with pillow python scripts/generate-brand-assets.py
```

## 使用规则

- 图形标最小显示尺寸为 24px；低于 24px 使用 `favicon.svg`。
- 组合标最小建议宽度为 180px。
- 四周净空至少为图形标宽度的 1/8。
- 不拉伸、不旋转、不增加阴影或渐变，不改变三种品牌色之间的角色。
- 深色背景使用原版；浅色背景可将组合标的纸白色 `#E8E2D4` 替换为墨蓝色 `#0E1A24`，其余颜色保持不变。
