import {
    Alert,
    Box,
    Button,
    Chip,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControl,
    FormControlLabel,
    InputLabel,
    MenuItem,
    Select,
    Stack,
    Switch,
    TextField,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import {
    createSource,
    type S3Config,
    type SFTPConfig,
    type SFTPCredentials,
    type SourceConfig,
    type SourceCredentials,
    type SourceResponse,
    type SourceType,
    type UpdateSourceInput,
    updateSource,
    type WebDAVConfig,
} from '../../api'

interface SourceDialogProps {
    open: boolean
    // 编辑时传入现有 Source；创建时为 null。
    source: SourceResponse | null
    onClose: () => void
    onSaved: (source: SourceResponse) => void
}

// secretKey 标识一个可三态更新的 secret 字段。
type SecretKey =
    | 'webdav.password'
    | 's3.secret_key'
    | 'sftp.password'
    | 'sftp.private_key'
    | 'sftp.passphrase'

// SourceDialog 创建 / 编辑 Source。创建时选择协议类型并按类型动态
// 表单；编辑时 Type readonly（协议不支持原地转换）。secret 绝不回填：
// 保持空白且未修改则不发送；输入新值即替换；Clear 明确清除（空串）。
export default function SourceDialog({ open, source, onClose, onSaved }: SourceDialogProps) {
    const [name, setName] = useState('')
    const [type, setType] = useState<SourceType>('webdav')
    const [enabled, setEnabled] = useState(true)
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)

    // 各协议非敏感配置字段。
    const [webdav, setWebdav] = useState<WebDAVConfig>({ endpoint: '', username: '' })
    const [s3, setS3] = useState<S3Config>({
        endpoint: '',
        region: '',
        bucket: '',
        prefix: '',
        path_style: true,
        access_key: '',
    })
    const [sftp, setSftp] = useState<SFTPConfig>({
        host: '',
        port: 22,
        username: '',
        remote_root: '',
        auth_method: 'password',
        host_key_fingerprint: '',
    })

    // secret 输入与三态标记：dirty 表示实际编辑，cleared 表示显式清除。
    const [secrets, setSecrets] = useState<Record<SecretKey, string>>({
        'webdav.password': '',
        's3.secret_key': '',
        'sftp.password': '',
        'sftp.private_key': '',
        'sftp.passphrase': '',
    })
    const [secretsDirty, setSecretsDirty] = useState<Set<SecretKey>>(new Set())
    const [secretsCleared, setSecretsCleared] = useState<Set<SecretKey>>(new Set())

    useEffect(() => {
        if (!open) {
            return
        }
        setName(source?.name ?? '')
        setType(source?.type ?? 'webdav')
        setEnabled(source?.enabled ?? true)
        setWebdav({
            endpoint:
                source?.type === 'webdav' ? ((source.config as WebDAVConfig).endpoint ?? '') : '',
            username:
                source?.type === 'webdav' ? ((source.config as WebDAVConfig).username ?? '') : '',
        })
        setS3(
            source?.type === 's3'
                ? (() => {
                      const cfg = source.config as S3Config
                      return {
                          endpoint: cfg.endpoint ?? '',
                          region: cfg.region ?? '',
                          bucket: cfg.bucket ?? '',
                          prefix: cfg.prefix ?? '',
                          path_style: cfg.path_style ?? true,
                          access_key: cfg.access_key ?? '',
                      }
                  })()
                : {
                      endpoint: '',
                      region: '',
                      bucket: '',
                      prefix: '',
                      path_style: true,
                      access_key: '',
                  },
        )
        setSftp(
            source?.type === 'sftp'
                ? (() => {
                      const cfg = source.config as SFTPConfig
                      return {
                          host: cfg.host ?? '',
                          port: cfg.port ?? 22,
                          username: cfg.username ?? '',
                          remote_root: cfg.remote_root ?? '',
                          auth_method: cfg.auth_method ?? 'password',
                          host_key_fingerprint: cfg.host_key_fingerprint ?? '',
                      }
                  })()
                : {
                      host: '',
                      port: 22,
                      username: '',
                      remote_root: '',
                      auth_method: 'password',
                      host_key_fingerprint: '',
                  },
        )
        setSecrets({
            'webdav.password': '',
            's3.secret_key': '',
            'sftp.password': '',
            'sftp.private_key': '',
            'sftp.passphrase': '',
        })
        setSecretsDirty(new Set())
        setSecretsCleared(new Set())
        setSaving(false)
        setError(null)
    }, [open, source])

    function setSecret(key: SecretKey, value: string) {
        setSecrets(prev => ({ ...prev, [key]: value }))
        setSecretsDirty(prev => new Set(prev).add(key))
        setSecretsCleared(prev => {
            const next = new Set(prev)
            next.delete(key)
            return next
        })
    }

    function clearSecret(key: SecretKey) {
        setSecrets(prev => ({ ...prev, [key]: '' }))
        setSecretsDirty(prev => new Set(prev).add(key))
        setSecretsCleared(prev => new Set(prev).add(key))
    }

    function secretState(key: SecretKey): { configured: boolean; label: string | null } {
        if (source === null) {
            return { configured: false, label: null }
        }
        const configured = isSecretConfigured(source, key)
        if (secretsCleared.has(key)) {
            return { configured, label: '保存后将清除' }
        }
        return { configured, label: configured ? '已配置' : '未设置' }
    }

    function buildConfig(): SourceConfig {
        if (type === 'webdav') {
            return { ...webdav }
        }
        if (type === 's3') {
            return { ...s3 }
        }
        return { ...sftp }
    }

    function buildCredentials(): SourceCredentials | undefined {
        if (type === 'webdav') {
            if (secretsDirty.has('webdav.password')) {
                return { password: secrets['webdav.password'] }
            }
            return undefined
        }
        if (type === 's3') {
            if (secretsDirty.has('s3.secret_key')) {
                return { secret_key: secrets['s3.secret_key'] }
            }
            return undefined
        }
        const creds: SFTPCredentials = {}
        let changed = false
        for (const key of ['sftp.password', 'sftp.private_key', 'sftp.passphrase'] as const) {
            if (!secretsDirty.has(key)) {
                continue
            }
            changed = true
            if (key === 'sftp.password') {
                creds.password = secrets[key]
            } else if (key === 'sftp.private_key') {
                creds.private_key = secrets[key]
            } else {
                creds.private_key_passphrase = secrets[key]
            }
        }
        return changed ? creds : undefined
    }

    async function handleSave() {
        setSaving(true)
        setError(null)
        try {
            if (source === null) {
                const credentials = buildCredentials()
                const created = await createSource({
                    name,
                    type,
                    config: buildConfig(),
                    credentials:
                        credentials !== undefined && Object.keys(credentials).length > 0
                            ? credentials
                            : undefined,
                    enabled,
                })
                onSaved(created)
                return
            }
            const patch: UpdateSourceInput = {}
            if (name !== source.name) {
                patch.name = name
            }
            const nextConfig = buildConfig()
            if (JSON.stringify(nextConfig) !== JSON.stringify(source.config)) {
                patch.config = nextConfig
            }
            const credentials = buildCredentials()
            if (credentials !== undefined) {
                patch.credentials = credentials
            }
            if (enabled !== source.enabled) {
                patch.enabled = enabled
            }
            const updated = await updateSource(source.id, patch)
            onSaved(updated)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setSaving(false)
        }
    }

    function canSave(): boolean {
        if (name.trim() === '') {
            return false
        }
        if (type === 'webdav') {
            return webdav.endpoint.trim() !== ''
        }
        if (type === 's3') {
            return s3.region.trim() !== '' && s3.bucket.trim() !== '' && s3.access_key.trim() !== ''
        }
        return (
            sftp.host.trim() !== '' &&
            sftp.username.trim() !== '' &&
            sftp.remote_root.trim() !== '' &&
            sftp.host_key_fingerprint.trim() !== ''
        )
    }

    const secretChip = (key: SecretKey, label: string) => {
        if (source === null) {
            return null
        }
        const state = secretState(key)
        return (
            <Chip
                size="small"
                label={`${label}：${state.label ?? '未设置'}`}
                color={secretsCleared.has(key) ? 'warning' : 'default'}
            />
        )
    }

    return (
        <Dialog
            open={open}
            onClose={onClose}
            scroll="paper"
            slotProps={{
                paper: {
                    sx: {
                        width: '60vw',
                        height: '80vh',
                        maxWidth: 'none',
                        maxHeight: 'none',
                    },
                },
            }}
        >
            <DialogTitle>{source === null ? '创建同步源' : '编辑同步源'}</DialogTitle>
            <DialogContent sx={{ flex: 1, overflowY: 'auto' }}>
                <Stack spacing={2} sx={{ pt: 1 }}>
                    {error !== null && <Alert severity="error">{error}</Alert>}
                    <TextField
                        label="名称"
                        value={name}
                        onChange={e => setName(e.target.value)}
                        required
                        autoFocus
                    />
                    <FormControl fullWidth>
                        <InputLabel id="source-type-label">类型</InputLabel>
                        <Select
                            labelId="source-type-label"
                            value={type}
                            label="类型"
                            disabled={source !== null}
                            onChange={e => setType(e.target.value as SourceType)}
                        >
                            <MenuItem value="webdav">WebDAV</MenuItem>
                            <MenuItem value="s3">S3</MenuItem>
                            <MenuItem value="sftp">SFTP</MenuItem>
                        </Select>
                    </FormControl>
                    {source !== null && (
                        <Typography variant="caption" color="text.secondary">
                            创建后不能修改同步源类型。
                        </Typography>
                    )}

                    {type === 'webdav' && (
                        <>
                            <TextField
                                label="服务地址"
                                value={webdav.endpoint}
                                onChange={e => setWebdav({ ...webdav, endpoint: e.target.value })}
                                required
                                placeholder="https://nas.example.com:5006/dav"
                                sx={{ '& input': { fontFamily: 'monospace' } }}
                            />
                            <TextField
                                label="用户名"
                                value={webdav.username}
                                onChange={e => setWebdav({ ...webdav, username: e.target.value })}
                            />
                            <SecretField
                                label="密码"
                                value={secrets['webdav.password']}
                                dirty={secretsDirty.has('webdav.password')}
                                cleared={secretsCleared.has('webdav.password')}
                                chip={secretChip('webdav.password', '密码')}
                                onChange={v => setSecret('webdav.password', v)}
                                onClear={() => clearSecret('webdav.password')}
                            />
                        </>
                    )}

                    {type === 's3' && (
                        <>
                            <TextField
                                label="服务地址（可选，留空使用 AWS 默认地址）"
                                value={s3.endpoint ?? ''}
                                onChange={e => setS3({ ...s3, endpoint: e.target.value })}
                                placeholder="https://s3.example.com"
                                sx={{ '& input': { fontFamily: 'monospace' } }}
                            />
                            <TextField
                                label="区域"
                                value={s3.region}
                                onChange={e => setS3({ ...s3, region: e.target.value })}
                                required
                                placeholder="us-east-1"
                            />
                            <TextField
                                label="存储桶"
                                value={s3.bucket}
                                onChange={e => setS3({ ...s3, bucket: e.target.value })}
                                required
                                sx={{ '& input': { fontFamily: 'monospace' } }}
                            />
                            <TextField
                                label="前缀（可选）"
                                value={s3.prefix ?? ''}
                                onChange={e => setS3({ ...s3, prefix: e.target.value })}
                                placeholder="tinysync"
                                sx={{ '& input': { fontFamily: 'monospace' } }}
                            />
                            <FormControlLabel
                                control={
                                    <Switch
                                        checked={s3.path_style ?? true}
                                        onChange={e =>
                                            setS3({ ...s3, path_style: e.target.checked })
                                        }
                                    />
                                }
                                label="路径风格寻址（MinIO / 自托管）"
                            />
                            <TextField
                                label="访问密钥"
                                value={s3.access_key}
                                onChange={e => setS3({ ...s3, access_key: e.target.value })}
                                required
                                sx={{ '& input': { fontFamily: 'monospace' } }}
                            />
                            <SecretField
                                label="访问密钥密码"
                                value={secrets['s3.secret_key']}
                                dirty={secretsDirty.has('s3.secret_key')}
                                cleared={secretsCleared.has('s3.secret_key')}
                                chip={secretChip('s3.secret_key', '访问密钥密码')}
                                onChange={v => setSecret('s3.secret_key', v)}
                                onClear={() => clearSecret('s3.secret_key')}
                            />
                        </>
                    )}

                    {type === 'sftp' && (
                        <>
                            <TextField
                                label="主机"
                                value={sftp.host}
                                onChange={e => setSftp({ ...sftp, host: e.target.value })}
                                required
                                placeholder="nas.example.com"
                            />
                            <TextField
                                label="端口"
                                type="number"
                                value={sftp.port ?? 22}
                                onChange={e => setSftp({ ...sftp, port: Number(e.target.value) })}
                            />
                            <TextField
                                label="用户名"
                                value={sftp.username}
                                onChange={e => setSftp({ ...sftp, username: e.target.value })}
                                required
                            />
                            <TextField
                                label="远端根目录"
                                value={sftp.remote_root}
                                onChange={e => setSftp({ ...sftp, remote_root: e.target.value })}
                                required
                                placeholder="/srv/backups"
                                sx={{ '& input': { fontFamily: 'monospace' } }}
                            />
                            <FormControl fullWidth>
                                <InputLabel id="sftp-auth-label">认证方式</InputLabel>
                                <Select
                                    labelId="sftp-auth-label"
                                    value={sftp.auth_method}
                                    label="认证方式"
                                    onChange={e =>
                                        setSftp({
                                            ...sftp,
                                            auth_method: e.target
                                                .value as SFTPConfig['auth_method'],
                                        })
                                    }
                                >
                                    <MenuItem value="password">密码</MenuItem>
                                    <MenuItem value="private_key">私钥</MenuItem>
                                </Select>
                            </FormControl>
                            <TextField
                                label="主机密钥指纹（SHA256）"
                                value={sftp.host_key_fingerprint}
                                onChange={e =>
                                    setSftp({ ...sftp, host_key_fingerprint: e.target.value })
                                }
                                required
                                placeholder="SHA256:UC1Dk4I9LLQOV3B8eZ5FlrUUcbbNie4INffe2TDTz3k"
                                sx={{ '& input': { fontFamily: 'monospace' } }}
                                helperText="必须验证主机密钥；未知主机的连接会失败。"
                            />
                            {sftp.auth_method === 'password' ? (
                                <SecretField
                                    label="密码"
                                    value={secrets['sftp.password']}
                                    dirty={secretsDirty.has('sftp.password')}
                                    cleared={secretsCleared.has('sftp.password')}
                                    chip={secretChip('sftp.password', '密码')}
                                    onChange={v => setSecret('sftp.password', v)}
                                    onClear={() => clearSecret('sftp.password')}
                                />
                            ) : (
                                <>
                                    <SecretField
                                        label="私钥（PEM）"
                                        value={secrets['sftp.private_key']}
                                        dirty={secretsDirty.has('sftp.private_key')}
                                        cleared={secretsCleared.has('sftp.private_key')}
                                        chip={secretChip('sftp.private_key', '私钥')}
                                        onChange={v => setSecret('sftp.private_key', v)}
                                        onClear={() => clearSecret('sftp.private_key')}
                                        multiline
                                    />
                                    <SecretField
                                        label="私钥口令（可选）"
                                        value={secrets['sftp.passphrase']}
                                        dirty={secretsDirty.has('sftp.passphrase')}
                                        cleared={secretsCleared.has('sftp.passphrase')}
                                        chip={secretChip('sftp.passphrase', '私钥口令')}
                                        onChange={v => setSecret('sftp.passphrase', v)}
                                        onClear={() => clearSecret('sftp.passphrase')}
                                    />
                                </>
                            )}
                        </>
                    )}

                    <FormControlLabel
                        control={
                            <Switch
                                checked={enabled}
                                onChange={e => setEnabled(e.target.checked)}
                            />
                        }
                        label="启用"
                    />
                </Stack>
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose} disabled={saving}>
                    取消
                </Button>
                <Button
                    onClick={() => void handleSave()}
                    variant="contained"
                    disabled={saving || !canSave()}
                >
                    {saving ? '保存中…' : '保存'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}

// SecretField 是带状态 Chip 与 Clear 按钮的 secret 输入：编辑时保持
// 空白即保留现有值；有 chip 说明当前 Source 已配置该 secret。
function SecretField({
    label,
    value,
    dirty,
    cleared,
    chip,
    onChange,
    onClear,
    multiline,
}: {
    label: string
    value: string
    dirty: boolean
    cleared: boolean
    chip: React.ReactNode
    onChange: (value: string) => void
    onClear: () => void
    multiline?: boolean
}) {
    return (
        <Box>
            <TextField
                label={label}
                type={multiline ? undefined : 'password'}
                value={value}
                onChange={e => onChange(e.target.value)}
                fullWidth
                multiline={multiline}
                rows={multiline ? 4 : undefined}
                placeholder={!dirty ? '留空以保留当前值' : undefined}
                sx={multiline ? { '& textarea': { fontFamily: 'monospace' } } : undefined}
            />
            <Box
                sx={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    mt: 1,
                }}
            >
                {chip ?? <Box />}
                {dirty && !cleared && (
                    <Button size="small" onClick={onClear}>
                        清除
                    </Button>
                )}
            </Box>
        </Box>
    )
}

// isSecretConfigured 从响应 credential_state 读取 secret 是否已设置。
function isSecretConfigured(source: SourceResponse, key: SecretKey): boolean {
    const state = source.credential_state
    switch (key) {
        case 'webdav.password':
            return state.webdav?.password_set ?? false
        case 's3.secret_key':
            return state.s3?.secret_key_set ?? false
        case 'sftp.password':
            return state.sftp?.password_set ?? false
        case 'sftp.private_key':
            return state.sftp?.private_key_set ?? false
        case 'sftp.passphrase':
            return state.sftp?.private_key_passphrase_set ?? false
    }
}
