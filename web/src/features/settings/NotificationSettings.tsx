import { Alert, CircularProgress, Stack } from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import {
    type EmailSettingsPatch,
    type NotificationSettingsResponse,
    type PushoverSettingsPatch,
    fetchNotificationSettings,
    testNotification,
    updateNotificationSettings,
} from '../../api'
import { useToast } from '../../app/toast'
import EmailSettings from './EmailSettings'
import PushoverSettings from './PushoverSettings'

// NotificationSettings 是设置页「通知」tab：Pushover 与 Email 两张
// 配置卡。数据按渠道独立保存（PATCH 部分更新），保存成功即生效，
// 服务端下次通知直接使用新配置。
export default function NotificationSettings() {
    const toast = useToast()
    const [settings, setSettings] = useState<NotificationSettingsResponse | null>(null)
    const [loadError, setLoadError] = useState<string | null>(null)
    const [saving, setSaving] = useState(false)
    const [testing, setTesting] = useState(false)

    useEffect(() => {
        let cancelled = false
        fetchNotificationSettings()
            .then(data => {
                if (!cancelled) setSettings(data)
            })
            .catch(reason => {
                if (!cancelled) {
                    setLoadError(reason instanceof Error ? reason.message : '通知配置加载失败')
                }
            })
        return () => {
            cancelled = true
        }
    }, [])

    const save = useCallback(
        async (patch: { pushover?: PushoverSettingsPatch; email?: EmailSettingsPatch }, name: string) => {
            setSaving(true)
            try {
                const updated = await updateNotificationSettings(patch)
                setSettings(updated)
                toast.success(`${name} 配置已保存。`)
            } catch (reason) {
                toast.error(reason instanceof Error ? reason.message : `${name} 配置保存失败`)
            } finally {
                setSaving(false)
            }
        },
        [toast],
    )

    const test = useCallback(
        async (channel: 'pushover' | 'email', name: string) => {
            setTesting(true)
            try {
                const result = await testNotification(channel)
                if (result.ok) toast.success(`${name} 测试通知已发出，请检查是否收到。`)
                else toast.error(`${name} 测试发送失败：${result.error ?? '未知错误'}`)
            } catch (reason) {
                toast.error(reason instanceof Error ? reason.message : `${name} 测试发送失败`)
            } finally {
                setTesting(false)
            }
        },
        [toast],
    )

    if (loadError !== null) {
        return <Alert severity="error">{loadError}</Alert>
    }
    if (settings === null) {
        return (
            <Stack sx={{ alignItems: 'center', py: 6 }}>
                <CircularProgress size={28} />
            </Stack>
        )
    }

    return (
        <Stack spacing={3}>
            <PushoverSettings
                settings={settings.pushover}
                saving={saving}
                testing={testing}
                onSave={patch => void save({ pushover: patch }, 'Pushover')}
                onTest={() => test('pushover', 'Pushover')}
            />
            <EmailSettings
                settings={settings.email}
                saving={saving}
                testing={testing}
                onSave={patch => void save({ email: patch }, 'Email')}
                onTest={() => test('email', 'Email')}
            />
        </Stack>
    )
}
