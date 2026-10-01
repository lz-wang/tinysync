import ForwardToInboxOutlinedIcon from '@mui/icons-material/ForwardToInboxOutlined'
import {
    Button,
    Card,
    CardContent,
    Chip,
    Divider,
    FormControlLabel,
    InputAdornment,
    MenuItem,
    Stack,
    Switch,
    TextField,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import type { EmailSettingsPatch, EmailSettingsResponse, NotificationSecurity } from '../../api'
import SecretField from '../sources/SecretField'

// EmailSettings 是 SMTP 邮件渠道配置卡：none / starttls / tls 三种
// 安全模式，收件人按逗号分隔输入；password 三态同 Pushover。
export default function EmailSettings({
    settings,
    saving,
    testing,
    onSave,
    onTest,
}: {
    settings: EmailSettingsResponse
    saving: boolean
    testing: boolean
    onSave: (patch: EmailSettingsPatch) => void
    onTest: () => Promise<void>
}) {
    const [enabled, setEnabled] = useState(settings.enabled)
    const [host, setHost] = useState(settings.host)
    const [port, setPort] = useState(String(settings.port))
    const [security, setSecurity] = useState<NotificationSecurity>(settings.security)
    const [username, setUsername] = useState(settings.username)
    const [from, setFrom] = useState(settings.from)
    const [to, setTo] = useState(settings.to.join(', '))
    const [password, setPassword] = useState('')
    const [clearPassword, setClearPassword] = useState(false)

    // 保存成功后 settings 刷新：表单回到「已保存」态。
    useEffect(() => {
        setEnabled(settings.enabled)
        setHost(settings.host)
        setPort(String(settings.port))
        setSecurity(settings.security)
        setUsername(settings.username)
        setFrom(settings.from)
        setTo(settings.to.join(', '))
        setPassword('')
        setClearPassword(false)
    }, [settings])

    const portNumber = Number.parseInt(port, 10)
    const portValid = Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65535

    const dirty =
        enabled !== settings.enabled ||
        host !== settings.host ||
        portNumber !== settings.port ||
        security !== settings.security ||
        username !== settings.username ||
        from !== settings.from ||
        to !== settings.to.join(', ') ||
        password !== '' ||
        clearPassword !== false

    const save = () => {
        const patch: EmailSettingsPatch = {
            enabled,
            host,
            port: portNumber,
            security,
            username,
            from,
            to: to
                .split(/[,，]/)
                .map(item => item.trim())
                .filter(item => item !== ''),
        }
        if (clearPassword) patch.clear_password = true
        else if (password !== '') patch.password = password
        onSave(patch)
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
                        <Typography variant="h6">Email</Typography>
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
                    <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
                        <TextField
                            label="SMTP Host"
                            value={host}
                            onChange={event => setHost(event.target.value)}
                            sx={{ flexGrow: 3 }}
                        />
                        <TextField
                            label="端口"
                            value={port}
                            error={!portValid}
                            helperText={!portValid ? '1–65535' : undefined}
                            onChange={event => setPort(event.target.value.replace(/[^0-9]/g, ''))}
                            sx={{ flexGrow: 1 }}
                            slotProps={{
                                input: {
                                    endAdornment: (
                                        <InputAdornment position="end">
                                            <Typography variant="caption" color="text.secondary">
                                                {security === 'tls'
                                                    ? '465'
                                                    : security === 'starttls'
                                                      ? '587'
                                                      : '25'}
                                            </Typography>
                                        </InputAdornment>
                                    ),
                                },
                            }}
                        />
                        <TextField
                            select
                            label="安全"
                            value={security}
                            onChange={event =>
                                setSecurity(event.target.value as NotificationSecurity)
                            }
                            sx={{ flexGrow: 2 }}
                        >
                            <MenuItem value="none">无加密</MenuItem>
                            <MenuItem value="starttls">STARTTLS</MenuItem>
                            <MenuItem value="tls">TLS</MenuItem>
                        </TextField>
                    </Stack>
                    <TextField
                        label="用户名"
                        value={username}
                        onChange={event => setUsername(event.target.value)}
                        helperText="匿名 SMTP 可留空"
                    />
                    <SecretField
                        label="密码"
                        value={password}
                        dirty={password !== ''}
                        cleared={false}
                        chip={
                            clearPassword ? (
                                <Chip
                                    label="保存后将删除"
                                    size="small"
                                    color="warning"
                                    variant="outlined"
                                />
                            ) : settings.password_configured ? (
                                <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
                                    <Typography variant="caption" color="text.secondary">
                                        已配置；留空保持不变
                                    </Typography>
                                    <Button
                                        size="small"
                                        color="error"
                                        onClick={() => setClearPassword(true)}
                                    >
                                        删除
                                    </Button>
                                </Stack>
                            ) : (
                                <Typography variant="caption" color="text.secondary">
                                    未配置
                                </Typography>
                            )
                        }
                        onChange={value => {
                            setPassword(value)
                            if (value !== '') setClearPassword(false)
                        }}
                        onClear={() => setPassword('')}
                    />
                    <TextField
                        label="发件地址（From）"
                        value={from}
                        onChange={event => setFrom(event.target.value)}
                    />
                    <TextField
                        label="收件地址（To）"
                        value={to}
                        onChange={event => setTo(event.target.value)}
                        helperText="多个地址以逗号分隔"
                    />
                    <Stack direction="row" spacing={1}>
                        <Button
                            variant="contained"
                            disabled={saving || !dirty || !portValid}
                            onClick={save}
                        >
                            保存
                        </Button>
                        <Button
                            startIcon={<ForwardToInboxOutlinedIcon />}
                            disabled={
                                dirty ||
                                saving ||
                                testing ||
                                !settings.enabled ||
                                !settings.host ||
                                !settings.from ||
                                settings.to.length === 0
                            }
                            onClick={() => void onTest()}
                        >
                            发送测试邮件
                        </Button>
                    </Stack>
                    <Typography variant="caption" color="text.secondary">
                        以纯文本发送任务完成摘要；测试邮件使用已保存的配置，配置保存后立即生效，无需重启。
                    </Typography>
                </Stack>
            </CardContent>
        </Card>
    )
}
