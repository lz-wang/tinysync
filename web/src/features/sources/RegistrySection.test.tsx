import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RegistryResponse } from '../../api'
import { createRegistry, deleteRegistry, listRegistries, updateRegistry } from '../../api'
import { ToastProvider } from '../../app/toast'
import RegistrySection from './RegistrySection'

// mock 整个 API 模块：Section 与内嵌的 RegistryDialog 都引用它。
vi.mock('../../api', () => ({
    createRegistry: vi.fn(),
    deleteRegistry: vi.fn(),
    listRegistries: vi.fn(),
    updateRegistry: vi.fn(),
}))

const mocked = vi.mocked({
    createRegistry,
    deleteRegistry,
    listRegistries,
    updateRegistry,
})

// primaryRow 构造默认形态的 primary 行：未配置移除前缀 / 路径转换 /
// 加速器。Go 仓储层把空列表归一为 nil，GET /registries 序列化为 null
// ——Section 表必须按可空列表兜底（回归：null 上的 .length 求值崩溃）。
function primaryRow(overrides: Partial<RegistryResponse> = {}): RegistryResponse {
    return {
        id: 'reg-1',
        name: 'hub',
        kind: 'primary',
        host: 'registry.lan:5000',
        namespace: '',
        username: '',
        password_set: false,
        insecure_skip_verify: false,
        plain_http: false,
        strip_registries: null,
        path_transform: null,
        mirrors: null,
        created_at: '2026-11-01T00:00:00Z',
        updated_at: '2026-11-01T00:00:00Z',
        ...overrides,
    }
}

function mirrorRow(): RegistryResponse {
    return {
        id: 'reg-2',
        name: 'docker 加速',
        kind: 'mirror',
        host: 'docker.io',
        namespace: '',
        username: '',
        password_set: false,
        insecure_skip_verify: false,
        plain_http: false,
        strip_registries: null,
        path_transform: null,
        mirrors: ['https://mirror.example.com'],
        created_at: '2026-11-01T00:00:00Z',
        updated_at: '2026-11-01T00:00:00Z',
    }
}

beforeEach(() => {
    mocked.listRegistries.mockResolvedValue([primaryRow(), mirrorRow()])
})

afterEach(() => {
    ;(document.activeElement as HTMLElement | null)?.blur?.()
    cleanup()
    vi.clearAllMocks()
})

describe('RegistrySection 可空列表渲染', () => {
    it('strip_registries / mirrors 为 null 的默认行不崩溃且不渲染前缀 chip', async () => {
        render(
            <ToastProvider>
                <RegistrySection />
            </ToastProvider>,
        )
        // 渲染完成即证明 null 未触发 .length 求值崩溃。
        expect(await screen.findByText('hub')).toBeTruthy()
        expect(screen.getByText('registry.lan:5000')).toBeTruthy()
        expect(screen.queryByText(/移除前缀/)).toBeNull()
        // mirror 行的加速地址正常 join。
        expect(screen.getByText('https://mirror.example.com')).toBeTruthy()
    })

    it('配置了移除前缀的行渲染计数 chip', async () => {
        mocked.listRegistries.mockResolvedValue([
            primaryRow({ strip_registries: ['docker.io/library', 'ghcr.io'] }),
        ])
        render(
            <ToastProvider>
                <RegistrySection />
            </ToastProvider>,
        )
        expect(await screen.findByText('移除前缀 ×2')).toBeTruthy()
    })
})
