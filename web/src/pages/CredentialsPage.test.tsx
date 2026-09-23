import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CredentialResponse, SourceResponse } from '../api'
import {
    createCredential,
    deleteCredential,
    listCredentials,
    listSources,
    updateCredential,
} from '../api'
import { ToastProvider } from '../app/toast'
import CredentialsPage from './CredentialsPage'

// mock 整个 API 模块：页面树内对话框也引用它。
vi.mock('../api', () => ({
    createCredential: vi.fn(),
    deleteCredential: vi.fn(),
    listCredentials: vi.fn(),
    listSources: vi.fn(),
    updateCredential: vi.fn(),
}))

const mocked = vi.mocked({
    createCredential,
    deleteCredential,
    listCredentials,
    listSources,
    updateCredential,
})

const credential: CredentialResponse = {
    id: 'crd-1',
    name: 'NAS 钥匙',
    type: 'ssh_key',
    fingerprint: 'SHA256:OJ9FPyy9KMBFzGfsIHU+6ahpI7MyCAmGhlvr+Vlu7DQ',
    has_passphrase: true,
    referenced_by: 1,
    created_at: '2026-09-23T00:00:00Z',
    updated_at: '2026-09-23T00:00:00Z',
}

const freeCredential: CredentialResponse = {
    ...credential,
    id: 'crd-2',
    name: '备用钥匙',
    fingerprint: 'SHA256:differ3ntFingerprintValue0000000000000000000000',
    has_passphrase: false,
    referenced_by: 0,
}

const referencingSource: SourceResponse = {
    id: 'src-1',
    name: 'NAS 源',
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
        sftp: { password_set: false, private_key_set: true, private_key_passphrase_set: true },
    },
    enabled: true,
    created_at: '2026-09-23T00:00:00Z',
    updated_at: '2026-09-23T00:00:00Z',
}

function renderPage() {
    return render(
        <MemoryRouter>
            <ToastProvider>
                <CredentialsPage />
            </ToastProvider>
        </MemoryRouter>,
    )
}

beforeEach(() => {
    mocked.listCredentials.mockResolvedValue([credential, freeCredential])
    mocked.listSources.mockResolvedValue([referencingSource])
})

afterEach(() => {
    cleanup()
    vi.clearAllMocks()
})

// 列表展示名称、指纹、口令与引用计数。
describe('CredentialsPage 列表', () => {
    it('渲染全部凭据与字段', async () => {
        renderPage()
        expect(await screen.findByText('NAS 钥匙')).toBeTruthy()
        expect(screen.getByText('备用钥匙')).toBeTruthy()
        expect(screen.getByText(/SHA256:OJ9FPyy9/)).toBeTruthy()
        expect(screen.getByText(/SHA256:differ3nt/)).toBeTruthy()
        expect(screen.getByText('带口令')).toBeTruthy()
        expect(screen.getByText('1 个源')).toBeTruthy()
        expect(screen.getByText('未引用')).toBeTruthy()
    })
})

// 创建流：填写名称与私钥后提交。
describe('CredentialsPage 创建', () => {
    it('提交创建请求并刷新列表', async () => {
        mocked.createCredential.mockResolvedValue(freeCredential)
        renderPage()
        await screen.findByText('NAS 钥匙')

        fireEvent.click(screen.getByRole('button', { name: '创建凭据' }))
        const nameInput = await screen.findByLabelText(/名称/)
        fireEvent.change(nameInput, { target: { value: '新钥匙' } })
        fireEvent.change(screen.getByLabelText(/私钥（OpenSSH PEM）/), {
            target: { value: '-----BEGIN OPENSSH PRIVATE KEY-----' },
        })
        fireEvent.click(screen.getByRole('button', { name: '保存' }))

        await waitFor(() => {
            expect(mocked.createCredential).toHaveBeenCalledWith({
                name: '新钥匙',
                type: 'ssh_key',
                secret: { private_key: '-----BEGIN OPENSSH PRIVATE KEY-----' },
            })
        })
        expect(mocked.listCredentials).toHaveBeenCalledTimes(2)
    })
})

// 编辑流：只改名不携带 secret。
describe('CredentialsPage 编辑', () => {
    it('改名提交不含 secret', async () => {
        mocked.updateCredential.mockResolvedValue(credential)
        renderPage()
        await screen.findByText('NAS 钥匙')

        fireEvent.click(screen.getByRole('button', { name: '编辑 NAS 钥匙' }))
        const nameInput = await screen.findByDisplayValue('NAS 钥匙')
        fireEvent.change(nameInput, { target: { value: '改名钥匙' } })
        fireEvent.click(screen.getByRole('button', { name: '保存' }))

        await waitFor(() => {
            expect(mocked.updateCredential).toHaveBeenCalledWith('crd-1', { name: '改名钥匙' })
        })
    })
})

// 删除流：被引用凭据禁删并列出引用源；无引用可删。
describe('CredentialsPage 删除', () => {
    it('被引用凭据展示引用源并禁用删除', async () => {
        renderPage()
        await screen.findByText('NAS 钥匙')

        fireEvent.click(screen.getByRole('button', { name: '删除 NAS 钥匙' }))

        expect(await screen.findByText(/正在引用此凭据/)).toBeTruthy()
        expect(screen.getByText('NAS 源')).toBeTruthy()
        expect(
            (screen.getByRole('button', { name: '删除' }) as HTMLButtonElement).disabled,
        ).toBeTruthy()
        expect(mocked.deleteCredential).not.toHaveBeenCalled()
        fireEvent.click(screen.getByRole('button', { name: '取消' }))
    })

    it('无引用凭据可删除', async () => {
        mocked.deleteCredential.mockResolvedValue(undefined)
        renderPage()
        await screen.findByText('备用钥匙')

        fireEvent.click(screen.getByRole('button', { name: '删除 备用钥匙' }))

        expect(await screen.findByText('删除凭据？')).toBeTruthy()
        fireEvent.click(screen.getByRole('button', { name: '删除' }))
        await waitFor(() => {
            expect(mocked.deleteCredential).toHaveBeenCalledWith('crd-2')
        })
    })
})
