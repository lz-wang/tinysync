import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { listLocalDirectoriesWithOptions } from '../../api'
import LocalDirectoryPicker from './LocalDirectoryPicker'

vi.mock('../../api', () => ({
    listLocalDirectoriesWithOptions: vi.fn(),
    createLocalDirectory: vi.fn(),
}))

const listDirectories = vi.mocked(listLocalDirectoriesWithOptions)
let pickerTrigger: HTMLButtonElement

beforeEach(() => {
    vi.resetAllMocks()
    // 模拟从浏览按钮打开对话框，给 MUI 焦点恢复提供真实的 DOM 目标。
    pickerTrigger = document.createElement('button')
    document.body.append(pickerTrigger)
    pickerTrigger.focus()
})
afterEach(() => {
    cleanup()
    pickerTrigger.remove()
})

describe('LocalDirectoryPicker 原生上级路径', () => {
    it.each([
        { path: '/home', parent: '/' },
        { path: 'C:\\Users', parent: 'C:\\' },
        { path: '\\\\server\\share\\folder', parent: '\\\\server\\share\\' },
        { path: '\\\\?\\C:\\Users', parent: '\\\\?\\C:\\' },
    ])('使用后端提供的 $path → $parent 并在根目录隐藏上级入口', async ({ path, parent }) => {
        listDirectories
            .mockResolvedValueOnce({ path, parent, directories: [] })
            .mockResolvedValueOnce({ path: parent, directories: [] })
        const onPick = vi.fn()
        render(<LocalDirectoryPicker open initialPath={path} onClose={() => {}} onPick={onPick} />)
        fireEvent.click(await screen.findByRole('button', { name: '上级目录' }))
        await waitFor(() => expect(listDirectories).toHaveBeenLastCalledWith(parent, false))
        await waitFor(() => expect(screen.queryByRole('button', { name: '上级目录' })).toBeNull())
        fireEvent.click(screen.getByRole('button', { name: '使用此目录' }))
        expect(onPick).toHaveBeenCalledWith(parent)
    })

    it('使用后端的目录名称显示并以完整路径下钻', async () => {
        listDirectories
            .mockResolvedValueOnce({
                path: '/data',
                parent: '/',
                directories: [{ name: '归档', path: '/data/archive' }],
            })
            .mockResolvedValueOnce({ path: '/data/archive', parent: '/data', directories: [] })
        render(
            <LocalDirectoryPicker open initialPath="/data" onClose={() => {}} onPick={() => {}} />,
        )
        fireEvent.click(await screen.findByRole('button', { name: '归档' }))
        await waitFor(() =>
            expect(listDirectories).toHaveBeenLastCalledWith('/data/archive', false),
        )
    })
})
