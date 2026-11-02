import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RegistryResponse } from '../../api'
import { createJob, listRegistries, updateJob } from '../../api'
import { ToastProvider } from '../../app/toast'
import JobDialog from './JobDialog'

// mock 整个 API 模块：JobDialog 引用 listRegistries / createJob / updateJob。
vi.mock('../../api', () => ({
    createJob: vi.fn(),
    listRegistries: vi.fn(),
    updateJob: vi.fn(),
}))

const mocked = vi.mocked({
    createJob,
    listRegistries,
    updateJob,
})

const registry: RegistryResponse = {
    id: 'reg-1',
    name: 'hub',
    kind: 'primary',
    host: 'docker.cnb.cool/lzwang/mirrors',
    namespace: 'lzwang/mirrors',
    username: '',
    password_set: false,
    insecure_skip_verify: false,
    plain_http: false,
    strip_registries: [],
    path_transform: null,
    mirrors: [],
    created_at: '2026-11-01T00:00:00Z',
    updated_at: '2026-11-01T00:00:00Z',
}

function renderDialog(props: Partial<Parameters<typeof JobDialog>[0]> = {}) {
    return render(
        <ToastProvider>
            <JobDialog
                open
                job={null}
                sources={[]}
                onClose={() => {}}
                onSaved={() => {}}
                {...props}
            />
        </ToastProvider>,
    )
}

// selectDstRegistry 在目标 Registry Autocomplete 中选中唯一的候选。
async function selectDstRegistry() {
    const input = screen.getByLabelText(/^目标 Registry/)
    fireEvent.mouseDown(input)
    fireEvent.click(await screen.findByRole('option'))
    // 选中后 chip 以 getOptionLabel 全名展示，弹层关闭。
    await waitFor(() => expect(screen.queryByRole('option')).toBeNull())
}

beforeEach(() => {
    mocked.listRegistries.mockResolvedValue([registry])
    mocked.createJob.mockResolvedValue({
        id: 'job-2',
        name: '镜像任务',
        kind: 'oci',
        enabled: true,
        config: { src_images: ['nginx:1.27'], dst_servers: ['hub'], platforms: [] },
        schedule: { type: 'manual' },
        created_at: '2026-11-02T00:00:00Z',
        updated_at: '2026-11-02T00:00:00Z',
    })
})

afterEach(() => {
    // jsdom 中上个用例的 activeElement 残留会让 Dialog FocusTrap 在
    // 卸载时对已分离节点调用 focus（MUI Modal 的 nodeToRestore），
    // 先把焦点归还 body 再清理。
    ;(document.activeElement as HTMLElement | null)?.blur?.()
    cleanup()
    vi.clearAllMocks()
})

// R34：OCI 表单字段采用 per-field validate() 错误映射；预填源镜像时类型
// 自动锁定为 oci。
describe('JobDialog OCI 表单校验（R34）', () => {
    it('预填源镜像时类型锁定 oci，缺目标 Registry 时展示逐字段错误且不提交', async () => {
        renderDialog({ initialOciImages: ['nginx:1.27'] })
        // 类型自动切到 OCI（select 展示值）。
        expect(screen.getByText('OCI 镜像同步')).toBeTruthy()
        fireEvent.change(screen.getByLabelText(/^名称/), { target: { value: '镜像任务' } })
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        expect(await screen.findByText('至少选择一个目标 Registry')).toBeTruthy()
        expect(mocked.createJob).not.toHaveBeenCalled()
    })

    it('空表单同时提示源镜像与目标 Registry 两处错误', async () => {
        renderDialog({ initialOciImages: ['nginx:1.27'] })
        fireEvent.change(screen.getByLabelText(/^名称/), { target: { value: '镜像任务' } })
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        expect(await screen.findByText('至少选择一个目标 Registry')).toBeTruthy()
    })

    it('平台过滤缺架构时展示行级错误', async () => {
        renderDialog({ initialOciImages: ['nginx:1.27'] })
        fireEvent.change(screen.getByLabelText(/^名称/), { target: { value: '镜像任务' } })
        await selectDstRegistry()
        fireEvent.click(screen.getByRole('button', { name: '添加平台' }))
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        expect(await screen.findByText('平台过滤的每一行都必须选择架构')).toBeTruthy()
        expect(mocked.createJob).not.toHaveBeenCalled()
    })

    it('合法表单按 oci 契约提交：src_images / dst_servers / platforms', async () => {
        renderDialog({ initialOciImages: ['nginx:1.27'] })
        fireEvent.change(screen.getByLabelText(/^名称/), { target: { value: '镜像任务' } })
        await selectDstRegistry()
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        await waitFor(() => expect(mocked.createJob).toHaveBeenCalledTimes(1))
        expect(mocked.createJob).toHaveBeenCalledWith({
            name: '镜像任务',
            kind: 'oci',
            config: { src_images: ['nginx:1.27'], dst_servers: ['hub'], platforms: [] },
            enabled: true,
            schedule: { type: 'manual' },
        })
    })

    it('git 类型不可选：菜单项禁用（规划中）', async () => {
        renderDialog()
        fireEvent.mouseDown(screen.getByLabelText('任务类型'))
        const gitOption = await screen.findByText('Git 仓库（规划中）')
        expect(gitOption.closest('li')?.getAttribute('aria-disabled')).toBe('true')
        // 关闭菜单，避免弹层残留到 cleanup。
        fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
        await waitFor(() => expect(screen.queryByText('Git 仓库（规划中）')).toBeNull())
    })
})
