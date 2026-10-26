// 看板列的宽度轨道（utils/planBoard.ts，issue gofer-5inz）：空列（0 张卡）只留列头与
// 计数，宽度约为普通列的三分之一，空出来的宽度归有卡片的列。
import { describe, expect, it } from 'vitest'
import { BOARD_COLUMNS, boardTracks, columnTrack } from './planBoard'

describe('columnTrack', () => {
  it('gives an empty column about a third of a normal column', () => {
    expect(columnTrack(0)).toBe('minmax(72px, 0.34fr)')
    expect(columnTrack(1)).toBe('minmax(220px, 1fr)')
    expect(columnTrack(7)).toBe('minmax(220px, 1fr)')
    // 「三分之一」：空列权重 0.34fr 对普通列 1fr。
    expect(columnTrack(0)).toContain('0.34fr')
    expect(columnTrack(3)).toContain('1fr')
  })

  it('keeps a minimum width so the narrow column is still a drop target', () => {
    expect(columnTrack(0)).toContain('minmax(72px')
  })
})

describe('boardTracks', () => {
  it('emits one track per board column, in column order', () => {
    const tracks = boardTracks([2, 0, 1, 0, 4])
    expect(tracks.match(/minmax\(/g)?.length).toBe(BOARD_COLUMNS.length)
    const parts = tracks.match(/minmax\([^)]*\)/g) ?? []
    expect(parts).toEqual([
      'minmax(220px, 1fr)',
      'minmax(72px, 0.34fr)',
      'minmax(220px, 1fr)',
      'minmax(72px, 0.34fr)',
      'minmax(220px, 1fr)',
    ])
  })

  it('narrows every column when the plan has no cards at all', () => {
    const tracks = boardTracks([0, 0, 0, 0, 0])
    expect(tracks).not.toContain('minmax(220px, 1fr)')
    expect(tracks.match(/0\.34fr/g)?.length).toBe(5)
  })
})
