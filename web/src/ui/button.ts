// 按钮的语义（设计稿 §8 的基元层）：类型放这里而不是 SFC 里——`<script setup>` 不能导出模块成员，
// 页面与测试都要能 import。
export type ButtonVariant = 'primary' | 'secondary' | 'ghost'
export type ButtonSize = 'sm' | 'md'

export const BUTTON_VARIANTS: readonly ButtonVariant[] = ['primary', 'secondary', 'ghost']
export const BUTTON_SIZES: readonly ButtonSize[] = ['sm', 'md']
