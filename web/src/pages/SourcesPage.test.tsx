import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
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

it('本地源显示 LOCAL 与原生路径，并可筛选和直达源端文件树', async () => {
    mocked.listSources.mockResolvedValue([
        webdavSource,
        {
            ...webdavSource,
            id: 'src-local',
            name: '本地照片',
            type: 'local',
            config: { root: '/data/photos' },
            credential_state: {},
        },
    ])
    renderPage()
    const link = await screen.findByRole('link', { name: '本地照片' })
    expect(screen.getByText('LOCAL')).toBeTruthy()
    expect(screen.getByText('/data/photos')).toBeTruthy()
    expect(link.getAttribute('href')).toContain('source=src-local')
    fireEvent.mouseDown(screen.getByRole('combobox', { name: '类型' }))
    fireEvent.click(await screen.findByRole('option', { name: '本地' }))
    await waitFor(() => expect(screen.queryByRole('link', { name: 'NAS WebDAV' })).toBeNull())
    expect(screen.getByRole('link', { name: '本地照片' })).toBeTruthy()
})

it('HTTP 源显示 HTTP 类型与 base_url 位置摘要，并可按类型筛选', async () => {
    mocked.listSources.mockResolvedValue([
        webdavSource,
        {
            ...webdavSource,
            id: 'src-http',
            name: '镜像站',
            type: 'http',
            config: {
                base_url: 'https://mirror.example.com/releases/',
                listing_mode: 'caddy',
                auth_method: 'none',
                caddy_file_limit: 10000,
            },
            credential_state: { http: { password_set: false, bearer_token_set: false } },
        },
    ])
    renderPage()
    const link = await screen.findByRole('link', { name: '镜像站' })
    expect(screen.getByText('HTTP')).toBeTruthy()
    // 位置摘要：base_url + 非 auto 的 listing profile。
    expect(screen.getByText('https://mirror.example.com/releases/ · caddy')).toBeTruthy()
    expect(link.getAttribute('href')).toContain('source=src-http')
    fireEvent.mouseDown(screen.getByRole('combobox', { name: '类型' }))
    fireEvent.click(await screen.findByRole('option', { name: 'HTTP' }))
    await waitFor(() => expect(screen.queryByRole('link', { name: 'NAS WebDAV' })).toBeNull())
    expect(screen.getByRole('link', { name: '镜像站' })).toBeTruthy()
})
