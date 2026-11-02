import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import MCPIntegrationPlaceholder from './MCPIntegrationPlaceholder'

// MCPIntegrationPlaceholder 是设置页的静态占位项（MCP 能力已移除，
// 统一任务管理稳定后再设计）。占位契约：只渲染静态文案，不提供任何
// 可交互控件，也不发起任何 API 请求（计划 §5：占位界面不读取配置、
// 不发送请求、不展示虚假的启用开关或连接状态）。
//
// apiFetch 是 api.ts 的模块私有函数且组件不 import 任何 API 模块，
// 因此以 stub global fetch 作为「零 API 请求」的忠实断言：所有网络
// 请求（apiFetch 或直连）最终都经过 global fetch。

afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
})

describe('MCPIntegrationPlaceholder 占位契约', () => {
    beforeEach(() => {
        vi.stubGlobal('fetch', vi.fn())
    })

    it('渲染「暂未开放」静态文案，不提供启用开关或连接状态控件', () => {
        render(<MCPIntegrationPlaceholder />)

        expect(screen.getByRole('heading', { name: 'MCP 集成' })).toBeTruthy()
        expect(screen.getByText('暂未开放')).toBeTruthy()
        expect(screen.getByText('计划在统一任务管理功能稳定后提供。')).toBeTruthy()

        // 无虚假控件：开关 / 按钮 / 输入 / 复选 / 进度与状态一律不存在。
        expect(screen.queryByRole('switch')).toBeNull()
        expect(screen.queryByRole('button')).toBeNull()
        expect(screen.queryByRole('checkbox')).toBeNull()
        expect(screen.queryByRole('textbox')).toBeNull()
        expect(screen.queryByRole('progressbar')).toBeNull()
        expect(screen.queryByRole('status')).toBeNull()
    })

    it('挂载与可见交互均不发送任何 API 请求', () => {
        const fetchMock = vi.fn()
        vi.stubGlobal('fetch', fetchMock)

        const { container } = render(<MCPIntegrationPlaceholder />)

        // 触发可见交互：对占位内每个可见元素派发点击 / 双击 / 键盘 /
        // 悬停事件，占位仍不得产生任何网络行为。
        for (const el of Array.from(container.querySelectorAll('*'))) {
            fireEvent.click(el)
            fireEvent.doubleClick(el)
            fireEvent.mouseOver(el)
            fireEvent.keyDown(el, { key: 'Enter' })
        }

        expect(fetchMock).not.toHaveBeenCalled()
    })
})
