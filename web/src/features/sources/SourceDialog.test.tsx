import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CredentialResponse, SFTPConfig, SourceResponse } from '../../api'
import { createSource, listCredentials, updateSource } from '../../api'
import SourceDialog from './SourceDialog'

// mock 整个 API 模块：SourceDialog 与 RemotePathPicker 都引用它。
vi.mock('../../api', () => ({
    createRemoteDirectory: vi.fn(),
    createSource: vi.fn(),
    listCredentials: vi.fn(),
    listRemoteFiles: vi.fn(),
    listSources: vi.fn(),
    updateSource: vi.fn(),
}))

const mocked = vi.mocked({
    createRemoteDirectory: await import('../../api').then(m => m.createRemoteDirectory),
    createSource,
    listCredentials,
    listRemoteFiles: await import('../../api').then(m => m.listRemoteFiles),
    listSources: await import('../../api').then(m => m.listSources),
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
        <SourceDialog open source={null} onClose={() => {}} onSaved={() => {}} {...props} />,
    )
}

beforeEach(() => {
    mocked.listCredentials.mockResolvedValue([credential])
    mocked.listSources.mockResolvedValue([referencingSource])
})

afterEach(() => {
    cleanup()
    vi.clearAllMocks()
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
