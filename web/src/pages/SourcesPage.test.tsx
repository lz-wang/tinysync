import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SourceResponse } from '../api'
import { deleteSource, listSources, testSource, updateSource } from '../api'
import { ToastProvider } from '../app/toast'
import SourcesPage from './SourcesPage'

// mock 整个 API 模块：页面树内 SourceDialog / DeleteSourceDialog 也引用它。
vi.mock('../api', () => ({
    deleteSource: vi.fn(),
    listSources: vi.fn(),
    testSource: vi.fn(),
    updateSource: vi.fn(),
}))

const mocked = vi.mocked({
    deleteSource,
    listSources,
    testSource,
    updateSource,
})

const webdavSource: SourceResponse = {
    id: 'src-1',
    name: 'NAS WebDAV',
    type: 'webdav',
    config: { endpoint: 'https://dav.example.com/', username: 'sync' },
    credential_state: { webdav: { password_set: true } },
    enabled: true,
    created_at: '2026-09-22T00:00:00Z',
    updated_at: '2026-09-22T00:00:00Z',
}

function renderPage() {
    return render(
        <MemoryRouter>
            <ToastProvider>
                <SourcesPage />
            </ToastProvider>
        </MemoryRouter>,
    )
}

beforeEach(() => {
    mocked.listSources.mockResolvedValue([webdavSource])
})

afterEach(cleanup)

describe('SourcesPage 文件浏览入口', () => {
    it('源名称链接到远端文件浏览（tab=remote 且携带 source 与根路径）', async () => {
        renderPage()
        const link = await screen.findByRole('link', { name: 'NAS WebDAV' })
        const href = link.getAttribute('href') ?? ''
        expect(href).not.toBe('')
        const params = new URLSearchParams(href.split('?')[1] ?? '')
        expect(params.get('tab')).toBe('remote')
        expect(params.get('source')).toBe('src-1')
        expect(params.get('path')).toBe('/')
    })

    it('名称是链接而非整行：其余单元格不含链接语义', async () => {
        renderPage()
        await screen.findByRole('link', { name: 'NAS WebDAV' })
        // 类型 Chip 与位置列都是纯文本，不产生第二个链接。
        const links = screen.queryAllByRole('link')
        expect(links).toHaveLength(1)
    })

    it('空列表不渲染链接', async () => {
        mocked.listSources.mockResolvedValue([])
        renderPage()
        await waitFor(() => expect(mocked.listSources).toHaveBeenCalled())
        expect(screen.queryByRole('link')).toBeNull()
    })
})
