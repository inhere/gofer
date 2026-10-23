import { defineConfig, mergeConfig } from 'vitest/config'
import viteConfig from './vite.config'

// 与 vite 共用一份配置，唯一不同：test.css = true。
// 默认（css: false）会把 CSS 模块变成空串，styles/cssVariables.test.ts 就读不到 tokens.css 的
// 正文——它要拿「tokens 里定义了哪些变量」当断言的基线。
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: { css: true },
  }),
)
