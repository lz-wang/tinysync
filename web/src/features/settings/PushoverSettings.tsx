import NotificationsActiveOutlinedIcon from '@mui/icons-material/NotificationsActiveOutlined'
import {
    Button,
    Card,
    CardContent,
    Chip,
    Divider,
    FormControlLabel,
    Stack,
    Switch,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import type { PushoverSettingsPatch, PushoverSettingsResponse } from '../../api'
import SecretField from '../sources/SecretField'

// PushoverSettings 是 Pushover 渠道配置卡：secret 三态（留空保留 /
// 清除删除 / 输入替换），保存与发送测试通知在同一卡片内完成。
export default function PushoverSettings({
    settings,
    saving,
    testing,
    onSave,
    onTest,
}: {
    settings: PushoverSettingsResponse
    saving: boolean
    testing: boolean
    onSave: (patch: PushoverSettingsPatch) => void
    onTest: () => Promise<void>
}) {
    const [enabled, setEnabled] = useState(settings.enabled)
    const [token, setToken] = useState('')
    const [clearToken, setClearToken] = useState(false)
    const [userKey, setUserKey] = useState('')
    const [clearUserKey, setClearUserKey] = useState(false)

    // 保存成功后 settings 刷新：表单回到「已保存」态。
    useEffect(() => {
        setEnabled(settings.enabled)
        setToken('')
        setClearToken(false)
        setUserKey('')
        setClearUserKey(false)
    }, [settings])

    const dirty =
        enabled !== settings.enabled ||
        token !== '' ||
        clearToken !== false ||
        userKey !== '' ||
        clearUserKey !== false

    const save = () => {
        const patch: PushoverSettingsPatch = { enabled }
        if (clearToken) patch.clear_token = true
        else if (token !== '') patch.token = token
        if (clearUserKey) patch.clear_user_key = true
        else if (userKey !== '') patch.user_key = userKey
        onSave(patch)
    }

    // secretChip 渲染 secret 状态提示：清除标记优先，其次已配置提示；
    // 已配置且未标记清除时提供「删除」入口（保存后生效）。删除启用
    // 渠道的必需 credential 本身意味着渠道无法继续启用——删除时同步
    // 关闭启用开关，避免产生一个必然被后端拒绝（enabled 但 secret
    // 缺失）的 PATCH。
    function secretChip(configured: boolean, markedClear: boolean, onDelete: () => void) {
        if (markedClear) {
            return <Chip label="保存后将删除" size="small" color="warning" variant="outlined" />
        }
        if (configured) {
            return (
                <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
                    <Typography variant="caption" color="text.secondary">
                        已配置；留空保持不变
                    </Typography>
                    <Button
                        size="small"
                        color="error"
                        onClick={() => {
                            setEnabled(false)
                            onDelete()
                        }}
                    >
                        删除
                    </Button>
                </Stack>
            )
        }
        return (
            <Typography variant="caption" color="text.secondary">
                未配置
            </Typography>
        )
    }

    return (
        <Card variant="outlined">
            <CardContent>
                <Stack spacing={2}>
                    <Stack
                        direction="row"
                        spacing={1}
                        sx={{ alignItems: 'center', justifyContent: 'space-between' }}
                    >
                        <Typography variant="h6">Pushover</Typography>
                        <FormControlLabel
                            control={
                                <Switch
                                    checked={enabled}
                                    onChange={event => setEnabled(event.target.checked)}
                                />
                            }
                            label="启用"
                            labelPlacement="start"
                        />
                    </Stack>
                    <Divider />
                    <SecretField
                        label="Application Token"
                        value={token}
                        dirty={token !== ''}
                        cleared={false}
                        chip={secretChip(settings.token_configured, clearToken, () =>
                            setClearToken(true),
                        )}
                        onChange={value => {
                            setToken(value)
                            if (value !== '') setClearToken(false)
                        }}
                        onClear={() => setToken('')}
                    />
                    <SecretField
                        label="User Key"
                        value={userKey}
                        dirty={userKey !== ''}
                        cleared={false}
                        chip={secretChip(settings.user_key_configured, clearUserKey, () =>
                            setClearUserKey(true),
                        )}
                        onChange={value => {
                            setUserKey(value)
                            if (value !== '') setClearUserKey(false)
                        }}
                        onClear={() => setUserKey('')}
                    />
                    <Stack direction="row" spacing={1}>
                        <Button variant="contained" disabled={saving || !dirty} onClick={save}>
                            保存
                        </Button>
                        <Button
                            startIcon={<NotificationsActiveOutlinedIcon />}
                            disabled={
                                dirty ||
                                saving ||
                                testing ||
                                !settings.enabled ||
                                !settings.token_configured ||
                                !settings.user_key_configured
                            }
                            onClick={() => void onTest()}
                        >
                            发送测试通知
                        </Button>
                    </Stack>
                    <Typography variant="caption" color="text.secondary">
                        任务完成（成功 / 失败 /
                        取消）后推送一条摘要；测试使用已保存的配置，配置保存后立即生效，无需重启。
                    </Typography>
                </Stack>
            </CardContent>
        </Card>
    )
}
