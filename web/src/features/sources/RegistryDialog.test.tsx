import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RegistryResponse } from '../../api'
import { createRegistry, updateRegistry } from '../../api'
import RegistryDialog from './RegistryDialog'

// mock 整个 API 模块：RegistryDialog 引用 createRegistry / updateRegistry。
vi.mock('../../api', () => ({
    createRegistry: vi.fn(),
    updateRegistry: vi.fn(),
}))

const mocked = vi.mocked({
    createRegistry,
    updateRegistry,
})

const existing: RegistryResponse = {
    id: 'reg-1',
    name: 'hub',
    kind: 'primary',
    host: 'registry.lan:5000',
    namespace: '',
    username: 'admin',
    password_set: true,
    insecure_skip_verify: true,
    plain_http: false,
    strip_registries: ['docker.io/library'],
    path_transform: null,
    mirrors: [],
    created_at: '2026-11-01T00:00:00Z',
    updated_at: '2026-11-01T00:00:00Z',
}

function renderDialog(props: Partial<Parameters<typeof RegistryDialog>[0]> = {}) {
    return render(
        <RegistryDialog
            open
            registry={null}
            createKind="primary"
            onClose={() => {}}
            onSaved={() => {}}
            {...props}
        />,
    )
}

beforeEach(() => {
    mocked.createRegistry.mockResolvedValue(existing)
    mocked.updateRegistry.mockResolvedValue(existing)
})

afterEach(() => {
    ;(document.activeElement as HTMLElement | null)?.blur?.()
    cleanup()
    vi.clearAllMocks()
})

// Registry/mirror 表单的 per-field 校验（R34）与三态 password 语义。
describe('RegistryDialog 校验', () => {
    it('空表单展示名称与地址两处错误且不提交', async () => {
        renderDialog()
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        expect(await screen.findByText('名称不能为空')).toBeTruthy()
        expect(screen.getByText('Registry 地址不能为空')).toBeTruthy()
        expect(mocked.createRegistry).not.toHaveBeenCalled()
    })

    it('合法 primary 表单按契约提交：path_transform 未启用为 null', async () => {
        renderDialog()
        fireEvent.change(screen.getByLabelText(/^名称/), { target: { value: 'hub' } })
        fireEvent.change(screen.getByLabelText(/^Registry 地址/), {
            target: { value: 'registry.lan:5000' },
        })
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        await waitFor(() => expect(mocked.createRegistry).toHaveBeenCalledTimes(1))
        expect(mocked.createRegistry).toHaveBeenCalledWith({
            name: 'hub',
            kind: 'primary',
            host: 'registry.lan:5000',
            insecure_skip_verify: false,
            plain_http: false,
            username: '',
            strip_registries: [],
            path_transform: null,
        })
    })

    it('启用路径转换但缺分隔符时展示错误', async () => {
        renderDialog()
        fireEvent.change(screen.getByLabelText(/^名称/), { target: { value: 'hub' } })
        fireEvent.change(screen.getByLabelText(/^Registry 地址/), {
            target: { value: 'registry.lan:5000' },
        })
        fireEvent.click(screen.getByLabelText('启用镜像路径转换'))
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        expect(await screen.findByText('启用路径转换后必须填写分隔符')).toBeTruthy()
        expect(mocked.createRegistry).not.toHaveBeenCalled()
    })
})

describe('RegistryDialog mirror 行', () => {
    it('缺加速地址时展示错误；补齐后按 mirrors 数组提交', async () => {
        renderDialog({ createKind: 'mirror' })
        fireEvent.change(screen.getByLabelText(/^名称/), { target: { value: 'docker 加速' } })
        fireEvent.change(screen.getByLabelText(/^源 Registry 主机/), {
            target: { value: 'docker.io' },
        })
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        expect(await screen.findByText('至少填写一个加速地址')).toBeTruthy()
        expect(mocked.createRegistry).not.toHaveBeenCalled()
        fireEvent.change(screen.getByLabelText(/^加速地址/), {
            target: { value: 'https://mirror.example.com, https://mirror2.example.com' },
        })
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        await waitFor(() => expect(mocked.createRegistry).toHaveBeenCalledTimes(1))
        expect(mocked.createRegistry).toHaveBeenCalledWith({
            name: 'docker 加速',
            kind: 'mirror',
            host: 'docker.io',
            insecure_skip_verify: false,
            plain_http: false,
            mirrors: ['https://mirror.example.com', 'https://mirror2.example.com'],
        })
    })
})

describe('RegistryDialog 编辑', () => {
    it('密码留空保留（PATCH 不带 password），勾选清除则发送空串', async () => {
        renderDialog({ registry: existing })
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        await waitFor(() => expect(mocked.updateRegistry).toHaveBeenCalledTimes(1))
        const first = vi.mocked(mocked.updateRegistry).mock.calls[0][1]
        expect(first.password).toBeUndefined()
        // 勾选清除已保存的密码后再次提交。
        fireEvent.click(screen.getByLabelText('清除已保存的密码'))
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        await waitFor(() => expect(mocked.updateRegistry).toHaveBeenCalledTimes(2))
        const second = vi.mocked(mocked.updateRegistry).mock.calls[1][1]
        expect(second.password).toBe('')
        expect(second.path_transform).toBeNull()
        expect(second.strip_registries).toEqual(['docker.io/library'])
    })
})
