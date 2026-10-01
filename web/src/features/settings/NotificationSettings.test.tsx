import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { NotificationSettingsResponse } from '../../api'
import { fetchNotificationSettings, testNotification, updateNotificationSettings } from '../../api'
import { ToastProvider } from '../../app/toast'
import NotificationSettings from './NotificationSettings'

// mock API 模块：组件只依赖这三个函数。
vi.mock('../../api', () => ({
    fetchNotificationSettings: vi.fn(),
    testNotification: vi.fn(),
    updateNotificationSettings: vi.fn(),
}))

const mocked = vi.mocked({
    fetchNotificationSettings,
    testNotification,
    updateNotificationSettings,
})

// configuredSettings 是两渠道完整配置的已保存态。
function configuredSettings(): NotificationSettingsResponse {
    return {
        pushover: {
            enabled: true,
            token_configured: true,
            user_key_configured: true,
        },
        email: {
            enabled: true,
            host: 'smtp.example.com',
            port: 587,
            security: 'starttls',
            username: 'tinysync@example.com',
            password_configured: true,
            from: 'tinysync@example.com',
            to: ['me@example.com'],
        },
        updated_at: '2026-10-01T00:00:00Z',
    }
}

// disabledButConfigured：数据库未启用但 secret 已配置——评审指出的
// 状态交叉形态：本地开关打开但未保存时，测试按钮必须保持禁用。
function disabledButConfigured(): NotificationSettingsResponse {
    const settings = configuredSettings()
    settings.pushover.enabled = false
    settings.email.enabled = false
    return settings
}

function renderPage() {
    return render(
        <MemoryRouter>
            <ToastProvider>
                <NotificationSettings />
            </ToastProvider>
        </MemoryRouter>,
    )
}

beforeEach(() => {
    mocked.fetchNotificationSettings.mockResolvedValue(configuredSettings())
    mocked.updateNotificationSettings.mockImplementation(async patch => ({
        ...configuredSettings(),
        ...(patch.pushover?.enabled !== undefined
            ? { pushover: { ...configuredSettings().pushover, enabled: patch.pushover.enabled } }
            : {}),
    }))
    mocked.testNotification.mockResolvedValue({ ok: true })
})

afterEach(cleanup)

describe('NotificationSettings 保存与测试的状态一致性', () => {
    it('已保存且已启用的渠道：测试按钮可用并可发起测试', async () => {
        renderPage()
        const pushoverTest = await screen.findByRole('button', { name: /发送测试通知/ })
        await waitFor(() => expect((pushoverTest as HTMLButtonElement).disabled).toBe(false))
        fireEvent.click(pushoverTest)
        await waitFor(() => expect(mocked.testNotification).toHaveBeenCalledWith('pushover'))
    })

    it('存在未保存修改时禁用测试：本地开关打开不等于已保存启用', async () => {
        mocked.fetchNotificationSettings.mockResolvedValue(disabledButConfigured())
        renderPage()
        const pushoverTest = await screen.findByRole('button', { name: /发送测试通知/ })
        // 已保存态：未启用 → 按钮禁用。
        await waitFor(() => expect((pushoverTest as HTMLButtonElement).disabled).toBe(true))

        // 本地打开 Pushover 开关（尚未保存）：按钮必须仍然禁用——
        // 后端测试只认已保存配置，此时点击只会得到 400。
        const switches = screen.getAllByRole('switch')
        fireEvent.click(switches[0]) // 第一张卡（Pushover）的启用开关
        expect((pushoverTest as HTMLButtonElement).disabled).toBe(true)

        // Email 同理：未保存修改时禁用。
        const emailTest = screen.getByRole('button', { name: /发送测试邮件/ })
        expect((emailTest as HTMLButtonElement).disabled).toBe(true)
    })

    it('保存成功后（settings 刷新）测试按钮恢复可用', async () => {
        mocked.fetchNotificationSettings.mockResolvedValue(disabledButConfigured())
        // 保存后返回启用态。
        mocked.updateNotificationSettings.mockResolvedValue(configuredSettings())
        renderPage()
        const pushoverTest = await screen.findByRole('button', { name: /发送测试通知/ })
        await waitFor(() => expect((pushoverTest as HTMLButtonElement).disabled).toBe(true))

        const switches = screen.getAllByRole('switch')
        fireEvent.click(switches[0])
        const save = screen.getAllByRole('button', { name: '保存' })[0]
        await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false))
        fireEvent.click(save)

        // settings 刷新为启用态且表单回到已保存：测试按钮恢复。
        await waitFor(() => expect((pushoverTest as HTMLButtonElement).disabled).toBe(false))
    })

    it('Email 修改 Host/Port/To 未保存时测试按钮同样禁用', async () => {
        renderPage()
        const emailTest = await screen.findByRole('button', { name: /发送测试邮件/ })
        await waitFor(() => expect((emailTest as HTMLButtonElement).disabled).toBe(false))

        // 修改 Host（未保存）：Email 卡进入 dirty，测试按钮禁用。
        const hostField = await screen.findByLabelText('SMTP Host')
        fireEvent.change(hostField, { target: { value: 'smtp2.example.com' } })
        await waitFor(() => expect((emailTest as HTMLButtonElement).disabled).toBe(true))

        // Email dirty 不影响 Pushover 的测试按钮（各卡独立判断）。
        const pushoverTest = screen.getByRole('button', { name: /发送测试通知/ })
        expect((pushoverTest as HTMLButtonElement).disabled).toBe(false)
    })

    it('删除 Pushover 已配置 token 时自动关闭启用开关', async () => {
        renderPage()
        await screen.findByRole('button', { name: /发送测试通知/ })

        fireEvent.click(screen.getAllByRole('button', { name: '删除' })[0])

        // 启用开关被联动关闭（dirty：保存按钮可用），保存的 patch
        // 携带 enabled=false 与 clear_token。
        const save = screen.getAllByRole('button', { name: '保存' })[0]
        await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false))
        fireEvent.click(save)
        await waitFor(() =>
            expect(mocked.updateNotificationSettings).toHaveBeenCalledWith({
                pushover: expect.objectContaining({
                    enabled: false,
                    clear_token: true,
                }),
            }),
        )
    })
})
