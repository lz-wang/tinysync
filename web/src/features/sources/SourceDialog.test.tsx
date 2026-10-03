import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CredentialResponse, SFTPConfig, SMBConfig, SourceResponse } from '../../api'
import {
    checkSFTPSource,
    createLocalDirectory,
    createSource,
    listCredentials,
    listLocalDirectoriesWithOptions,
    promoteSourceCredential,
    updateSource,
} from '../../api'
import { ToastProvider } from '../../app/toast'
import SourceDialog from './SourceDialog'

// mock 整个 API 模块：SourceDialog 与 RemotePathPicker 都引用它。
vi.mock('../../api', () => ({
    checkSFTPSource: vi.fn(),
    createRemoteDirectory: vi.fn(),
    createSource: vi.fn(),
    listCredentials: vi.fn(),
    listLocalDirectoriesWithOptions: vi.fn(),
    createLocalDirectory: vi.fn(),
    listRemoteFiles: vi.fn(),
    listSources: vi.fn(),
    promoteSourceCredential: vi.fn(),
    updateSource: vi.fn(),
}))

const mocked = vi.mocked({
    checkSFTPSource,
    createRemoteDirectory: await import('../../api').then(m => m.createRemoteDirectory),
    createSource,
    listCredentials,
    listLocalDirectoriesWithOptions,
    createLocalDirectory,
    listRemoteFiles: await import('../../api').then(m => m.listRemoteFiles),
    listSources: await import('../../api').then(m => m.listSources),
    promoteSourceCredential,
    updateSource,
})

const credential: CredentialResponse = {
    id: 'crd-1',
    name: 'NAS 钥匙',
    type: 'ssh_key',
    fingerprint: 'SHA256:OJ9FPyy9KMBFzGfsIHU',
    has_passphrase: false,
    referenced_by: 1,
    created_at: '2026-09-23T00:00:00Z',
    updated_at: '2026-09-23T00:00:00Z',
}

// 引用态的存量 SFTP 源。
const referencingSource: SourceResponse = {
    id: 'src-1',
    name: 'NAS',
    type: 'sftp',
    config: {
        host: 'nas.example.com',
        port: 22,
        username: 'tinysync',
        remote_root: '/',
        auth_method: 'private_key',
        host_key_fingerprint: '',
        credential_id: 'crd-1',
    },
    credential_state: {
        sftp: { password_set: false, private_key_set: true, private_key_passphrase_set: false },
    },
    enabled: true,
    created_at: '2026-09-23T00:00:00Z',
    updated_at: '2026-09-23T00:00:00Z',
}

function renderDialog(props: Partial<Parameters<typeof SourceDialog>[0]> = {}) {
    return render(
        <ToastProvider>
            <SourceDialog open source={null} onClose={() => {}} onSaved={() => {}} {...props} />
        </ToastProvider>,
    )
}

beforeEach(() => {
    mocked.checkSFTPSource.mockResolvedValue({ ok: true, latency_ms: 8 })
    mocked.listCredentials.mockResolvedValue([credential])
    mocked.listSources.mockResolvedValue([referencingSource])
})

afterEach(() => {
    cleanup()
    vi.clearAllMocks()
})

describe('SourceDialog SFTP 根目录检查', () => {
    async function fillPasswordForm() {
        renderDialog()
        fireEvent.mouseDown(screen.getByLabelText(/类型/))
        fireEvent.click(await screen.findByText('SFTP'))
        expect(screen.queryByRole('button', { name: '浏览' })).toBeNull()
        expect((screen.getByRole('button', { name: '检查' }) as HTMLButtonElement).disabled).toBe(
            true,
        )
        fireEvent.change(screen.getByPlaceholderText('nas.example.com'), {
            target: { value: 'new.example.com' },
        })
        fireEvent.change(screen.getByLabelText(/端口/), { target: { value: '2222' } })
        fireEvent.change(screen.getByLabelText(/用户名/), { target: { value: 'new-user' } })
        fireEvent.change(screen.getByLabelText(/^密码/), { target: { value: 'new-password' } })
    }

    it('创建前使用最新表单检查，不依赖名称、不保存同步源', async () => {
        await fillPasswordForm()
        fireEvent.change(screen.getByLabelText(/源端路径/), { target: { value: '/srv/files' } })
        fireEvent.click(screen.getByRole('button', { name: '检查' }))
        await waitFor(() =>
            expect(mocked.checkSFTPSource).toHaveBeenCalledWith({
                source_id: undefined,
                config: {
                    host: 'new.example.com',
                    port: 2222,
                    username: 'new-user',
                    remote_root: '/srv/files',
                    auth_method: 'password',
                    host_key_fingerprint: '',
                    credential_id: '',
                },
                credentials: { password: 'new-password' },
            }),
        )
        expect(await screen.findByText(/SFTP 检查通过.*srv\/files/)).toBeTruthy()
        expect(mocked.createSource).not.toHaveBeenCalled()
        expect(mocked.updateSource).not.toHaveBeenCalled()
    })

    it('根目录留空检查 Home，检查中禁止重复提交，失败经 toast 展示', async () => {
        let finish: ((result: Awaited<ReturnType<typeof checkSFTPSource>>) => void) | undefined
        mocked.checkSFTPSource.mockImplementation(
            () =>
                new Promise(resolve => {
                    finish = resolve
                }),
        )
        await fillPasswordForm()
        fireEvent.click(screen.getByRole('button', { name: '检查' }))
        expect(
            (screen.getByRole('button', { name: '检查中…' }) as HTMLButtonElement).disabled,
        ).toBe(true)
        expect(mocked.checkSFTPSource.mock.calls[0][0].config.remote_root).toBe('')
        finish?.({ ok: false, latency_ms: 10, error: 'permission denied' })
        expect(await screen.findByText('SFTP 检查失败：permission denied')).toBeTruthy()
        await waitFor(() =>
            expect(
                (screen.getByRole('button', { name: '检查' }) as HTMLButtonElement).disabled,
            ).toBe(false),
        )
    })

    it('编辑使用提案根目录及引用，未改动的秘密字段不回填', async () => {
        renderDialog({ source: referencingSource })
        fireEvent.change(screen.getByLabelText(/源端路径/), { target: { value: '/new-root' } })
        fireEvent.click(screen.getByRole('button', { name: '检查' }))
        await waitFor(() =>
            expect(mocked.checkSFTPSource).toHaveBeenCalledWith({
                source_id: 'src-1',
                config: { ...referencingSource.config, remote_root: '/new-root' },
                credentials: undefined,
            }),
        )
        expect(mocked.updateSource).not.toHaveBeenCalled()
    })

    it('内联私钥及解密口令随当前表单提交，显式清除私钥后禁止检查', async () => {
        renderDialog({
            source: {
                ...referencingSource,
                config: { ...referencingSource.config, credential_id: '' },
            },
        })
        fireEvent.change(screen.getByLabelText(/^私钥（PEM）/), { target: { value: 'NEW_PEM' } })
        fireEvent.change(screen.getByLabelText(/^私钥口令/), {
            target: { value: 'NEW_PASSPHRASE' },
        })
        fireEvent.click(screen.getByRole('button', { name: '检查' }))
        await waitFor(() =>
            expect(mocked.checkSFTPSource.mock.calls[0][0].credentials).toEqual({
                private_key: 'NEW_PEM',
                private_key_passphrase: 'NEW_PASSPHRASE',
            }),
        )
        await screen.findByText(/SFTP 检查通过/)
        fireEvent.click(screen.getAllByRole('button', { name: /清除/ })[0])
        expect((screen.getByRole('button', { name: '检查' }) as HTMLButtonElement).disabled).toBe(
            true,
        )
    })

    it('请求异常显示失败通知', async () => {
        mocked.checkSFTPSource.mockRejectedValueOnce(new Error('invalid remote_root'))
        await fillPasswordForm()
        fireEvent.click(screen.getByRole('button', { name: '检查' }))
        expect(await screen.findByText('SFTP 检查失败：invalid remote_root')).toBeTruthy()
    })
})

// 编辑引用态源：私钥来源为凭据库并回显凭据名与指纹；换绑另一凭据
// 后保存的 config 携带新 credential_id。
describe('SourceDialog 凭据选择器', () => {
    it('引用态回显凭据，换绑写入新 credential_id', async () => {
        mocked.updateSource.mockImplementation(
            async (_id: string, _input: Parameters<typeof updateSource>[1]) => {
                return referencingSource
            },
        )
        renderDialog({ source: referencingSource })

        // 来源为凭据库且回显凭据名 + 指纹。
        fireEvent.mouseDown(await screen.findByText('NAS 钥匙 · SHA256:OJ9FPyy9KMBFzGfsIHU'))

        // 切换到内联：保存的 config credential_id 为空。
        fireEvent.mouseDown(screen.getByLabelText(/私钥来源/))
        fireEvent.click(await screen.findByText('粘贴一次性私钥'))
        fireEvent.click(screen.getByRole('button', { name: '保存' }))

        await waitFor(() => {
            expect(mocked.updateSource).toHaveBeenCalled()
        })
        const input = mocked.updateSource.mock.calls[0][1]
        const config = input.config as SFTPConfig
        expect(config.credential_id).toBe('')
    })

    it('凭据库模式下未选凭据时保存禁用', async () => {
        renderDialog()

        fireEvent.change(screen.getByLabelText(/名称/), { target: { value: '新源' } })
        fireEvent.mouseDown(screen.getByLabelText(/类型/))
        fireEvent.click(await screen.findByText('SFTP'))
        fireEvent.change(screen.getByPlaceholderText('nas.example.com'), {
            target: { value: 'nas.example.com' },
        })
        fireEvent.change(screen.getByLabelText(/用户名/), { target: { value: 'tinysync' } })
        fireEvent.mouseDown(screen.getByLabelText(/认证方式/))
        fireEvent.click(await screen.findByText('私钥'))
        fireEvent.mouseDown(screen.getByLabelText(/私钥来源/))
        fireEvent.click(await screen.findByText('从凭据库选择'))

        await waitFor(() => {
            expect(
                (screen.getByRole('button', { name: '保存' }) as HTMLButtonElement).disabled,
            ).toBeTruthy()
        })
        expect(mocked.createSource).not.toHaveBeenCalled()
    })

    it('创建请求携带选中的 credential_id', async () => {
        mocked.createSource.mockResolvedValue(referencingSource)
        renderDialog()

        fireEvent.change(screen.getByLabelText(/名称/), { target: { value: '新源' } })
        fireEvent.mouseDown(screen.getByLabelText(/类型/))
        fireEvent.click(await screen.findByText('SFTP'))
        fireEvent.change(screen.getByPlaceholderText('nas.example.com'), {
            target: { value: 'nas.example.com' },
        })
        fireEvent.change(screen.getByLabelText(/用户名/), { target: { value: 'tinysync' } })
        fireEvent.mouseDown(screen.getByLabelText(/认证方式/))
        fireEvent.click(await screen.findByText('私钥'))
        fireEvent.mouseDown(screen.getByLabelText(/私钥来源/))
        fireEvent.click(await screen.findByText('从凭据库选择'))
        fireEvent.mouseDown(screen.getByLabelText(/凭据/))
        fireEvent.click(await screen.findByText(/NAS 钥匙 · SHA256:OJ9FPyy9/))
        fireEvent.click(screen.getByRole('button', { name: '保存' }))

        await waitFor(() => {
            expect(mocked.createSource).toHaveBeenCalled()
        })
        const input = mocked.createSource.mock.calls[0][0]
        const config = input.config as SFTPConfig
        expect(config.credential_id).toBe('crd-1')
    })
})

// 提升为凭据：内联私钥源在编辑态可一键提升（票 #6）。
describe('SourceDialog 提升为凭据', () => {
    it('合格源展示提升按钮，提交调用 promote API', async () => {
        const inlineSource: SourceResponse = {
            ...referencingSource,
            id: 'src-inline',
            name: '内联源',
            config: {
                ...referencingSource.config,
                credential_id: '',
            },
        }
        mocked.promoteSourceCredential.mockResolvedValue({
            source: {
                ...inlineSource,
                config: { ...inlineSource.config, credential_id: 'crd-new' },
            },
            credential: {
                id: 'crd-new',
                name: '提升的钥匙',
                fingerprint: 'SHA256:new',
                has_passphrase: false,
            },
        })
        const onSaved = vi.fn()
        renderDialog({ source: inlineSource, onSaved })

        fireEvent.click(await screen.findByRole('button', { name: /提升为凭据/ }))
        fireEvent.change(await screen.findByLabelText(/凭据名称/), {
            target: { value: '提升的钥匙' },
        })
        fireEvent.click(screen.getByRole('button', { name: '提升' }))

        await waitFor(() => {
            expect(mocked.promoteSourceCredential).toHaveBeenCalledWith('src-inline', '提升的钥匙')
        })
        await waitFor(() => {
            expect(onSaved).toHaveBeenCalled()
        })
    })

    it('引用态源不展示提升按钮', async () => {
        renderDialog({ source: referencingSource })
        await waitFor(() => expect(mocked.listCredentials).toHaveBeenCalled())
        expect(screen.queryByRole('button', { name: /提升为凭据/ })).toBeNull()
    })
})

// SMB 表单：创建必填密码（guest 不支持）、默认值归一提交、编辑回显
// 与 signing 调整（非身份字段）。
const smbSource: SourceResponse = {
    id: 'src-smb',
    name: 'NAS SMB',
    type: 'smb',
    config: {
        host: 'nas.example.com',
        port: 445,
        share: 'backup',
        remote_root: '/photos',
        username: 'tinysync',
        domain: 'WORKGROUP',
        signing: 'required',
    },
    credential_state: {
        smb: { password_set: true },
    },
    enabled: true,
    created_at: '2026-10-03T00:00:00Z',
    updated_at: '2026-10-03T00:00:00Z',
}

describe('SourceDialog SMB', () => {
    it('创建：默认 signing=required 提交，密码随 credentials 发送', async () => {
        mocked.createSource.mockResolvedValue(smbSource)
        renderDialog()

        fireEvent.change(screen.getByLabelText(/名称/), { target: { value: 'NAS SMB' } })
        fireEvent.mouseDown(screen.getByLabelText(/类型/))
        fireEvent.click(await screen.findByText('SMB (SMB2/SMB3)'))
        fireEvent.change(screen.getByPlaceholderText('192.168.2.10'), {
            target: { value: 'nas.example.com' },
        })
        fireEvent.change(screen.getByLabelText(/共享名称/), {
            target: { value: 'backup' },
        })
        fireEvent.change(screen.getByLabelText(/用户名/), {
            target: { value: 'tinysync' },
        })
        fireEvent.change(screen.getByLabelText(/^密码/), { target: { value: 'smb-pass' } })
        fireEvent.click(screen.getByRole('button', { name: '保存' }))

        await waitFor(() => {
            expect(mocked.createSource).toHaveBeenCalled()
        })
        const input = mocked.createSource.mock.calls[0][0]
        const config = input.config as SMBConfig
        expect(config.host).toBe('nas.example.com')
        expect(config.share).toBe('backup')
        expect(config.port).toBe(445)
        expect(config.remote_root).toBe('/')
        expect(config.signing).toBe('required')
        expect(input.credentials).toEqual({ password: 'smb-pass' })
    })

    it('创建：无密码时保存禁用（guest 不支持）', async () => {
        renderDialog()

        fireEvent.change(screen.getByLabelText(/名称/), { target: { value: 'NAS SMB' } })
        fireEvent.mouseDown(screen.getByLabelText(/类型/))
        fireEvent.click(await screen.findByText('SMB (SMB2/SMB3)'))
        fireEvent.change(screen.getByPlaceholderText('192.168.2.10'), {
            target: { value: 'nas.example.com' },
        })
        fireEvent.change(screen.getByLabelText(/共享名称/), { target: { value: 'backup' } })
        fireEvent.change(screen.getByLabelText(/用户名/), { target: { value: 'tinysync' } })

        await waitFor(() => {
            expect(
                (screen.getByRole('button', { name: '保存' }) as HTMLButtonElement).disabled,
            ).toBeTruthy()
        })
        expect(mocked.createSource).not.toHaveBeenCalled()
    })

    it('编辑：回显 share / root / signing，调整 signing 后进入 patch config', async () => {
        mocked.updateSource.mockResolvedValue(smbSource)
        renderDialog({ source: smbSource })

        // 编辑存量源（密码已配置）即使不动密码也可保存。
        await waitFor(() => {
            expect(
                (screen.getByRole('button', { name: '保存' }) as HTMLButtonElement).disabled,
            ).toBeFalsy()
        })
        fireEvent.mouseDown(screen.getByLabelText(/消息签名/))
        fireEvent.click(await screen.findByText('服务器决定'))
        fireEvent.click(screen.getByRole('button', { name: '保存' }))

        await waitFor(() => {
            expect(mocked.updateSource).toHaveBeenCalled()
        })
        const input = mocked.updateSource.mock.calls[0][1]
        const config = input.config as SMBConfig
        expect(config.signing).toBe('auto')
        expect(config.share).toBe('backup')
        expect(config.remote_root).toBe('/photos')
        // 未触碰密码：不发送 credentials。
        expect(input.credentials).toBeUndefined()
    })
})

const localSource: SourceResponse = {
    id: 'src-local',
    name: '本地照片',
    type: 'local',
    config: { root: '/data/photos' },
    credential_state: {},
    enabled: true,
    created_at: '2026-10-03T00:00:00Z',
    updated_at: '2026-10-03T00:00:00Z',
}

describe('SourceDialog 本地文件源', () => {
    it('复用主机目录选择器，提交原生路径且不携带其他类型的已编辑凭据', async () => {
        mocked.createSource.mockResolvedValue(localSource)
        mocked.listLocalDirectoriesWithOptions.mockResolvedValue({
            path: '/data/photos',
            directories: [],
        })
        renderDialog()
        fireEvent.change(screen.getByLabelText(/名称/), { target: { value: '本地照片' } })
        // WebDAV 密码草稿不能在切换到 Local 后进入请求。
        fireEvent.change(screen.getByLabelText(/密码/), { target: { value: 'unused-password' } })
        fireEvent.mouseDown(screen.getByLabelText(/类型/))
        fireEvent.click(await screen.findByText('本地文件'))
        expect(screen.queryByLabelText(/密码|用户名|私钥|凭据/)).toBeNull()
        expect((screen.getByRole('button', { name: '保存' }) as HTMLButtonElement).disabled).toBe(
            true,
        )
        fireEvent.click(screen.getByRole('button', { name: '浏览' }))
        await waitFor(() =>
            expect(mocked.listLocalDirectoriesWithOptions).toHaveBeenCalledWith('', false),
        )
        fireEvent.click(await screen.findByRole('button', { name: '使用此目录' }))
        await waitFor(() => expect(screen.queryByText('选择本地目录')).toBeNull())
        expect((screen.getByLabelText(/源根目录/) as HTMLInputElement).value).toBe('/data/photos')
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        await waitFor(() => expect(mocked.createSource).toHaveBeenCalled())
        const input = mocked.createSource.mock.calls[0][0]
        expect(input.type).toBe('local')
        expect(input.config).toEqual({ root: '/data/photos' })
        expect(input.credentials).toBeUndefined()
    })

    it('编辑时保持类型只读并只提交修改后的源根目录', async () => {
        mocked.updateSource.mockResolvedValue(localSource)
        renderDialog({ source: localSource })
        expect(screen.getByRole('combobox', { name: '类型' }).getAttribute('aria-disabled')).toBe(
            'true',
        )
        expect(screen.queryByLabelText(/密码|用户名|私钥|凭据/)).toBeNull()
        fireEvent.change(screen.getByLabelText(/源根目录/), { target: { value: '/data/videos' } })
        fireEvent.click(screen.getByRole('button', { name: '保存' }))
        await waitFor(() =>
            expect(mocked.updateSource).toHaveBeenCalledWith('src-local', {
                config: { root: '/data/videos' },
            }),
        )
    })
})
